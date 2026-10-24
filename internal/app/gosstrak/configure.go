package gosstrak

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
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

// Pipeline builds the processing pipeline a configuration file describes: a
// translating consumer whose processor matches the file's subscriptions and,
// when the file names destinations, routes matches to them. The returned
// options give the App ownership of delivery.
//
// Secrets are resolved here, once, through lookup (os.LookupEnv in
// production). Any missing secret, invalid URL, uncompilable pattern or
// unknown route fails the whole pipeline, so a misconfigured deployment does
// not start. Errors name the offending field, never a secret or a URL.
func Pipeline(f configfile.File, lookup func(string) (string, bool), logger *slog.Logger) (*TranslatingConsumer, []Option, error) {
	subscriptions, routes, err := compileSubscriptions(f.Subscriptions)
	if err != nil {
		return nil, nil, err
	}
	var set filter.Set
	if err := set.Replace(subscriptions); err != nil {
		return nil, nil, fmt.Errorf("subscriptions: %w", err)
	}

	var options []Option
	var processorOptions []ProcessorOption
	if len(f.Destinations) > 0 {
		destinations, err := buildDestinations(f.Destinations, lookup)
		if err != nil {
			return nil, nil, err
		}
		dispatcher, err := report.New(ReportConfig(f), destinations, logger)
		if err != nil {
			return nil, nil, fmt.Errorf("report: %w", err)
		}
		processorOptions = append(processorOptions, DeliverTo(dispatcher, routes))
		options = append(options, WithDelivery(dispatcher))
	} else if len(routes) > 0 {
		for id := range routes {
			return nil, nil, fmt.Errorf("subscription %q routes to destinations, but none are configured", id)
		}
	}
	processor, err := NewMatchingProcessor(&set, logger, processorOptions...)
	if err != nil {
		return nil, nil, err
	}
	// Tags without a protocol-control word are assumed to be GS1 EPC data.
	// That assumption is explicit rather than inferred from the tag itself,
	// and readers that report the PC word are believed instead.
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	return NewTranslatingConsumer(translator, processor), options, nil
}

func compileSubscriptions(in []configfile.Subscription) ([]filter.Subscription, Routes, error) {
	subscriptions := make([]filter.Subscription, len(in))
	routes := Routes{}
	for i, s := range in {
		if len(s.Patterns) > filter.MaxPatternsPerSubscription {
			return nil, nil, fmt.Errorf("subscriptions[%d].patterns: %d exceeds %d", i, len(s.Patterns), filter.MaxPatternsPerSubscription)
		}
		patterns := make([]identity.Pattern, len(s.Patterns))
		for j, text := range s.Patterns {
			p, err := tdt.CompilePattern(text)
			if err != nil {
				return nil, nil, fmt.Errorf("subscriptions[%d].patterns[%d]: %w", i, j, err)
			}
			patterns[j] = p
		}
		subscriptions[i] = filter.Subscription{ID: filter.ID(s.ID), Patterns: patterns}
		if len(s.Destinations) > 0 {
			routes[filter.ID(s.ID)] = s.Destinations
		}
	}
	return subscriptions, routes, nil
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
			secret, err := source.Resolve(lookup)
			if err != nil {
				return nil, fmt.Errorf("destinations[%d].headers.%s: %w", i, name, err)
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
