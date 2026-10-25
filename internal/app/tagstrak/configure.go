package tagstrak

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/control"
	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/report"
	"github.com/iomz/tagstrak/v2/internal/report/httpreport"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// ApplyFile overlays the scalar settings a configuration file sets onto c.
// Settings the file omits keep c's values, so defaults, the file, the
// environment and flags can be layered in that order (#2).
func ApplyFile(c Config, f configfile.File) Config {
	setString(&c.Reader.Address, f.Reader.Address)
	setString(&c.Reader.Source, f.Reader.Source)
	setDuration(&c.Reader.ConnectTimeout, f.Reader.ConnectTimeout)
	setDuration(&c.Reader.HandshakeTimeout, f.Reader.HandshakeTimeout)
	setDuration(&c.Reader.ReadTimeout, f.Reader.ReadTimeout)
	setDuration(&c.Reader.WriteTimeout, f.Reader.WriteTimeout)
	setDuration(&c.Reader.RetryMin, f.Reader.RetryMin)
	setDuration(&c.Reader.RetryMax, f.Reader.RetryMax)
	setInt(&c.Reader.MaxRetries, f.Reader.MaxRetries)
	if f.Reader.MaxFrameBytes != nil {
		c.Reader.Limits.MaxFrameSize = *f.Reader.MaxFrameBytes
	}
	if f.Reader.MaxParameterBytes != nil {
		c.Reader.Limits.MaxParameterSize = *f.Reader.MaxParameterBytes
	}
	setInt(&c.Reader.Limits.MaxParameters, f.Reader.MaxParameters)
	setString(&c.TelemetryAddress, f.Telemetry.Address)
	setString(&c.Control.Socket, f.Control.Socket)
	setString(&c.Control.Address, f.Control.Address)
	setInt(&c.QueueCapacity, f.Runtime.QueueCapacity)
	setDuration(&c.ConsumeTimeout, f.Runtime.ConsumeTimeout)
	setDuration(&c.ShutdownTimeout, f.Runtime.ShutdownTimeout)
	return c
}

// ReportConfig overlays a file's report settings onto the delivery defaults.
func ReportConfig(f configfile.File) report.Config {
	c := report.DefaultConfig()
	setInt(&c.QueueCapacity, f.Report.QueueCapacity)
	setInt(&c.BatchEvents, f.Report.BatchEvents)
	setInt(&c.BatchBytes, f.Report.BatchBytes)
	setDuration(&c.FlushInterval, f.Report.FlushInterval)
	setDuration(&c.RequestTimeout, f.Report.RequestTimeout)
	setInt(&c.MaxAttempts, f.Report.MaxAttempts)
	setDuration(&c.BackoffBase, f.Report.BackoffBase)
	setDuration(&c.BackoffMax, f.Report.BackoffMax)
	setDuration(&c.RetryBudget, f.Report.RetryBudget)
	return c
}

// Assembly is the runtime a configuration file describes.
type Assembly struct {
	// Consumer translates and matches observations.
	Consumer *TranslatingConsumer
	// Options give the App ownership of delivery and control.
	Options []Option
	// Subscriptions is the live subscription table.
	Subscriptions *SubscriptionTable
	// Source is the subscription source, "file" or "api".
	Source string
}

// Pipeline builds the processing pipeline a configuration file describes: a
// subscription table loaded from the file or, in "api" mode, from the state
// file; delivery to the file's destinations; and, when control is enabled,
// the management API.
//
// Secrets are resolved here, once, through lookup (os.LookupEnv in
// production). Any missing secret, invalid URL or header, uncompilable
// pattern, unknown route or unreadable state file fails the whole pipeline,
// so a misconfigured deployment does not start. Errors name the offending
// field, never a secret or a URL.
func Pipeline(f configfile.File, c Config, lookup func(string) (string, bool), logger *slog.Logger) (Assembly, error) {
	names := make([]string, len(f.Destinations))
	for i, d := range f.Destinations {
		names[i] = d.Name
	}
	compile, err := matcherCompiler(f)
	if err != nil {
		return Assembly{}, err
	}
	table := NewSubscriptionTableWith(names, compile)
	source := f.SubscriptionSource()
	var store *StateStore
	switch source {
	case configfile.SourceAPI:
		// The file layer cannot see control flags, so the pairing is checked
		// here: without the API, nothing could ever change the subscriptions.
		if !c.Control.Enabled() {
			return Assembly{}, errors.New(`subscription_store.source "api" requires a control socket or address`)
		}
		store = &StateStore{Path: *f.Store.StateFile}
		state, err := configfile.LoadState(store.Path)
		if err != nil {
			return Assembly{}, err
		}
		if err := table.Load(state.Subscriptions, state.Revision); err != nil {
			return Assembly{}, fmt.Errorf("state file: %w", err)
		}
	default:
		if err := table.Load(f.Subscriptions, 1); err != nil {
			return Assembly{}, err
		}
	}

	var options []Option
	var processorOptions []ProcessorOption
	if len(f.Destinations) > 0 {
		destinations, err := buildDestinations(f.Destinations, lookup)
		if err != nil {
			return Assembly{}, err
		}
		dispatcher, err := report.New(ReportConfig(f), destinations, logger)
		if err != nil {
			return Assembly{}, fmt.Errorf("report: %w", err)
		}
		processorOptions = append(processorOptions, DeliverTo(dispatcher))
		options = append(options, WithDelivery(dispatcher))
	}
	if c.Control.Enabled() {
		var token []byte
		if f.Control.Token != nil {
			secret, err := f.Control.Token.Resolve(lookup)
			if err != nil {
				return Assembly{}, fmt.Errorf("control.token: %w", err)
			}
			token = secret.Reveal()
		}
		options = append(options, WithControl(ControlSettings{Token: token, Subscriptions: table, Source: source, Store: store}))
	} else if f.Control.Token != nil {
		return Assembly{}, fmt.Errorf("control.token is set, but no control socket or address is configured")
	}
	processor, err := NewMatchingProcessor(table, logger, processorOptions...)
	if err != nil {
		return Assembly{}, err
	}
	// Tags without a protocol-control word are assumed to be GS1 EPC data.
	// That assumption is explicit rather than inferred from the tag itself,
	// and readers that report the PC word are believed instead.
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	return Assembly{Consumer: NewTranslatingConsumer(translator, processor), Options: options, Subscriptions: table, Source: source}, nil
}

// StateStore persists API-managed subscriptions.
type StateStore struct {
	Path string
}

// Save writes the state file atomically.
func (s *StateStore) Save(revision uint64, spec []configfile.Subscription) error {
	return configfile.WriteState(s.Path, configfile.State{Revision: revision, Subscriptions: spec})
}

// ReloadFile re-reads the configuration file and replaces the subscriptions
// it lists, for SIGHUP in "file" mode. Only subscriptions are reloaded;
// destinations, secrets and other settings take effect on restart. Every
// attempt is audited with actor "signal". On error the current subscriptions
// stay in place.
func ReloadFile(ctx context.Context, path string, table *SubscriptionTable, logger *slog.Logger) error {
	before := table.Revision()
	audit := func(outcome, code string, after uint64, count int) {
		attrs := []slog.Attr{slog.Uint64("revision_before", before), slog.Uint64("revision_after", after), slog.Int("count", count)}
		if code != "" {
			attrs = append(attrs, slog.String("error_code", code))
		}
		control.AuditRecord(ctx, logger, "signal", "", "subscriptions.reload", outcome, attrs...)
	}
	f, err := configfile.Load(path)
	if err != nil {
		audit("rejected", "invalid_config", before, 0)
		return err
	}
	if f.SubscriptionSource() != configfile.SourceFile {
		audit("rejected", "source_changed", before, 0)
		return fmt.Errorf("subscription_store.source changed to %q; restart to change the source", f.SubscriptionSource())
	}
	after, err := table.Replace(f.Subscriptions, nil, nil)
	if err != nil {
		audit("rejected", "invalid_subscriptions", before, 0)
		return err
	}
	audit("ok", "", after, len(f.Subscriptions))
	return nil
}

// compilers maps the matcher names a file may use to their compilers.
var compilers = map[string]filter.Compiler{
	configfile.MatcherIndex:  filter.CompileIndex,
	configfile.MatcherLinear: filter.CompileLinear,
}

// matcherCompiler returns the compiler a file's [filter] section selects.
// Adaptive bounds belong to the filter package, so they are checked here by
// compiling an empty set, before anything starts.
//
// Load validates names and modes, but Pipeline also accepts files built in
// code, so both are checked again rather than trusted.
func matcherCompiler(f configfile.File) (filter.Compiler, error) {
	switch mode := f.FilterMode(); mode {
	case configfile.ModeSingle:
		compile, ok := compilers[f.FilterMatcher()]
		if !ok {
			return nil, &FieldError{Field: "filter.matcher", Err: fmt.Errorf("unknown matcher %q", f.FilterMatcher())}
		}
		return compile, nil
	case configfile.ModeAdaptive:
	default:
		return nil, &FieldError{Field: "filter.mode", Err: fmt.Errorf("unknown mode %q", mode)}
	}
	a := f.Filter.Adaptive
	names := a.Candidates
	if names == nil {
		names = []string{configfile.MatcherIndex, configfile.MatcherLinear}
	}
	cfg := filter.DefaultAdaptiveConfig()
	cfg.FullShadow = a.Shadow != nil && *a.Shadow == configfile.ShadowFull
	for i, name := range names {
		compile, ok := compilers[name]
		if !ok {
			return nil, &FieldError{Field: fmt.Sprintf("filter.adaptive.candidates[%d]", i), Err: fmt.Errorf("unknown matcher %q", name)}
		}
		cfg.Candidates = append(cfg.Candidates, filter.Candidate{Name: name, Compile: compile})
	}
	setInt(&cfg.SampleEvery, a.SampleEvery)
	setInt(&cfg.Warmup, a.Warmup)
	setInt(&cfg.Window, a.Window)
	setInt(&cfg.Cooldown, a.Cooldown)
	if a.Hysteresis != nil {
		cfg.Hysteresis = *a.Hysteresis
	}
	if a.OverheadBudget != nil {
		cfg.OverheadBudget = *a.OverheadBudget
	}
	if a.Experimental != nil {
		cfg.Experimental = *a.Experimental
	}
	if _, err := filter.CompileAdaptive(nil, cfg); err != nil {
		return nil, &FieldError{Field: "filter.adaptive", Err: err}
	}
	return cfg.Compiler(), nil
}

func buildDestinations(in []configfile.Destination, lookup func(string) (string, bool)) ([]report.Destination, error) {
	destinations := make([]report.Destination, len(in))
	for i, d := range in {
		// Credentials belong in header secrets, which are redacted; a URL
		// is configuration and must not carry them.
		if u, err := url.Parse(d.URL); err == nil && u.User != nil {
			return nil, fmt.Errorf("destinations[%d].url: user information is not allowed; use a header secret", i)
		}
		headers := make(map[string]configfile.Secret, len(d.Headers))
		for name, source := range d.Headers {
			// net/http rejects invalid headers per request, which would turn
			// a configuration error into a stream of failed deliveries.
			if !validHeaderName(name) {
				return nil, fmt.Errorf("destinations[%d].headers: %q is not a valid header name", i, name)
			}
			secret, err := source.Resolve(lookup)
			if err != nil {
				return nil, fmt.Errorf("destinations[%d].headers.%s: %w", i, name, err)
			}
			if !validHeaderValue(secret.Reveal()) {
				return nil, fmt.Errorf("destinations[%d].headers.%s: secret contains control characters", i, name)
			}
			headers[http.CanonicalHeaderKey(name)] = secret
		}
		reporter, err := httpreport.New(d.URL, httpreport.WithHeader(func(h http.Header) {
			for name, secret := range headers {
				h.Set(name, string(secret.Reveal()))
			}
		}))
		if err != nil {
			return nil, fmt.Errorf("destinations[%d].url: %w", i, err)
		}
		destinations[i] = report.Destination{Name: d.Name, Reporter: reporter}
	}
	return destinations, nil
}

// validHeaderName reports whether name is an RFC 9110 token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue reports whether value is free of control characters other
// than horizontal tab, so it cannot split or end a header line.
func validHeaderValue(value []byte) bool {
	for _, c := range value {
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

func setString(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}

func setInt(dst *int, v *int) {
	if v != nil {
		*dst = *v
	}
}

func setDuration(dst *time.Duration, v *configfile.Duration) {
	if v != nil {
		*dst = time.Duration(*v)
	}
}
