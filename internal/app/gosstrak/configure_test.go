package gosstrak

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/report"
)

func ptr[T any](v T) *T { return &v }

func dur(d time.Duration) *configfile.Duration { v := configfile.Duration(d); return &v }

// fullFile sets every scalar setting to a non-default value.
func fullFile() configfile.File {
	return configfile.File{
		Version: configfile.Version,
		Reader: configfile.Reader{
			Address: ptr("r:1"), Source: ptr("s"),
			ConnectTimeout: dur(11 * time.Second), HandshakeTimeout: dur(12 * time.Second),
			ReadTimeout: dur(13 * time.Second), WriteTimeout: dur(14 * time.Second),
			RetryMin: dur(15 * time.Millisecond), RetryMax: dur(16 * time.Second),
			MaxRetries: ptr(17), MaxFrameBytes: ptr(uint32(18000)), MaxParameterBytes: ptr(uint16(1900)), MaxParameters: ptr(20),
		},
		Telemetry: configfile.Telemetry{Address: ptr("127.0.0.1:21")},
		Runtime:   configfile.Runtime{QueueCapacity: ptr(22), ConsumeTimeout: dur(23 * time.Second), ShutdownTimeout: dur(24 * time.Second)},
		Report: configfile.Report{
			QueueCapacity: ptr(2500), BatchEvents: ptr(26), BatchBytes: ptr(27000),
			FlushInterval: dur(28 * time.Millisecond), RequestTimeout: dur(29 * time.Second), MaxAttempts: ptr(30),
			BackoffBase: dur(31 * time.Millisecond), BackoffMax: dur(32 * time.Second), RetryBudget: dur(33 * time.Second),
		},
	}
}

// TestFixtureSetsEverySetting guards the mapping against drift: adding a
// schema field without extending fullFile, and so the mapping test, fails.
func TestFixtureSetsEverySetting(t *testing.T) {
	f := fullFile()
	for _, section := range []any{f.Reader, f.Telemetry, f.Runtime, f.Report} {
		v := reflect.ValueOf(section)
		for i := range v.NumField() {
			if v.Field(i).IsNil() {
				t.Errorf("%s.%s is not set by fullFile", v.Type().Name(), v.Type().Field(i).Name)
			}
		}
	}
}

func TestApplyFileMapsEverySetting(t *testing.T) {
	if got := ApplyFile(DefaultConfig(), configfile.File{Version: configfile.Version}); !reflect.DeepEqual(got, DefaultConfig()) {
		t.Fatalf("an empty file changed defaults: %+v", got)
	}
	c := ApplyFile(DefaultConfig(), fullFile())
	r := c.Reader
	if r.Address != "r:1" || r.Source != "s" || r.ConnectTimeout != 11*time.Second || r.HandshakeTimeout != 12*time.Second ||
		r.ReadTimeout != 13*time.Second || r.WriteTimeout != 14*time.Second || r.RetryMin != 15*time.Millisecond || r.RetryMax != 16*time.Second ||
		r.MaxRetries != 17 || r.Limits.MaxFrameSize != 18000 || r.Limits.MaxParameterSize != 1900 || r.Limits.MaxParameters != 20 {
		t.Errorf("reader %+v", r)
	}
	if c.TelemetryAddress != "127.0.0.1:21" || c.QueueCapacity != 22 || c.ConsumeTimeout != 23*time.Second || c.ShutdownTimeout != 24*time.Second {
		t.Errorf("app %+v", c)
	}
	rc := ReportConfig(fullFile())
	want := report.Config{QueueCapacity: 2500, BatchEvents: 26, BatchBytes: 27000, FlushInterval: 28 * time.Millisecond,
		RequestTimeout: 29 * time.Second, MaxAttempts: 30, BackoffBase: 31 * time.Millisecond, BackoffMax: 32 * time.Second, RetryBudget: 33 * time.Second}
	if rc != want {
		t.Errorf("report %+v, want %+v", rc, want)
	}
	if got := ReportConfig(configfile.File{}); got != report.DefaultConfig() {
		t.Errorf("an empty file changed report defaults: %+v", got)
	}
}

// TestPipelineDeliversWithSecretHeader runs the configured pipeline against a
// real HTTP destination: subscriptions compile, routes resolve, and the
// header secret reaches the destination.
func TestPipelineDeliversWithSecretHeader(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer destination.Close()
	f := configfile.File{
		Version: configfile.Version,
		Report:  configfile.Report{FlushInterval: dur(5 * time.Millisecond)},
		Destinations: []configfile.Destination{{
			Name: "erp", URL: destination.URL,
			Headers: map[string]configfile.SecretSource{"authorization": {Env: "ERP_TOKEN", Prefix: "Bearer "}},
		}},
		Subscriptions: []configfile.Subscription{
			{ID: "product", Patterns: []string{"urn:epc:idpat:sgtin:0614141.812345.*"}, Destinations: []string{"erp"}},
			{ID: "unrouted", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}},
		},
	}
	assembly, err := Pipeline(f, DefaultConfig(), func(name string) (string, bool) { return "t0ken", name == "ERP_TOKEN" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer, options := assembly.Consumer, assembly.Options
	if len(options) != 1 {
		t.Fatalf("got %d app options, want delivery", len(options))
	}
	var a App
	options[0](&a)
	a.delivery.Start()
	if err := consumer.Consume(context.Background(), observation(t, "3074257bf7194e4000001a85", 0x3000, true)); err != nil {
		t.Fatal(err)
	}
	if err := a.delivery.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auth) != 1 || auth[0] != "Bearer t0ken" {
		t.Fatalf("Authorization headers %q", auth)
	}
}

func TestPipelineWithoutDestinationsOnlyMatches(t *testing.T) {
	assembly, err := Pipeline(configfile.File{
		Version:       configfile.Version,
		Subscriptions: []configfile.Subscription{{ID: "a", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}}},
	}, DefaultConfig(), noLookup, nil)
	if err != nil || len(assembly.Options) != 0 {
		t.Fatalf("options %d, err %v", len(assembly.Options), err)
	}
}

func noLookup(string) (string, bool) { return "", false }

// TestSubscriptionTableUsesItsMatcher checks that a table compiles every
// snapshot with the matcher it was built with, and that Pipeline builds the
// configured one.
func TestSubscriptionTableUsesItsMatcher(t *testing.T) {
	calls := 0
	counting := func(subs []filter.Subscription) (filter.Matcher, error) {
		calls++
		return filter.CompileLinear(subs)
	}
	table := NewSubscriptionTableWith(nil, counting)
	if err := table.Load([]configfile.Subscription{{ID: "a", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}}}, 1); err != nil {
		t.Fatal(err)
	}
	if calls != 2 { // the empty initial snapshot, then the load
		t.Fatalf("compiler called %d times, want 2", calls)
	}
	for matcher, want := range map[string]string{
		configfile.MatcherIndex:  "*filter.index",
		configfile.MatcherLinear: "*filter.linear",
	} {
		f := configfile.File{
			Version:       configfile.Version,
			Filter:        configfile.Filter{Matcher: ptr(matcher)},
			Subscriptions: []configfile.Subscription{{ID: "a", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}}},
		}
		assembly, err := Pipeline(f, DefaultConfig(), noLookup, nil)
		if err != nil {
			t.Fatalf("%s: %v", matcher, err)
		}
		// The built matcher, not the compiler function, so a Pipeline that
		// ignored the setting or swapped compilers fails here.
		if got := fmt.Sprintf("%T", assembly.Subscriptions.current.Load().matcher); got != want {
			t.Errorf("filter.matcher %q built %s, want %s", matcher, got, want)
		}
	}
}

// TestPipelineRejectsMisconfiguration checks that every misconfiguration
// stops startup with an error naming the field, never a secret or a URL.
func TestPipelineRejectsMisconfiguration(t *testing.T) {
	sub := func(patterns []string, destinations ...string) configfile.Subscription {
		return configfile.Subscription{ID: "a", Patterns: patterns, Destinations: destinations}
	}
	dest := configfile.Destination{Name: "erp", URL: "https://erp.example/hooks"}
	tooMany := make([]string, 65)
	for i := range tooMany {
		tooMany[i] = "urn:epc:idpat:sgtin:*.*.*"
	}
	for _, tc := range []struct {
		name string
		file configfile.File
		want string
	}{
		{"bad pattern", configfile.File{Subscriptions: []configfile.Subscription{sub([]string{"urn:epc:idpat:sgtin:*.*.*"}), {ID: "b", Patterns: []string{"urn:nope"}}}}, "subscriptions[1].patterns[0]"},
		{"too many patterns", configfile.File{Subscriptions: []configfile.Subscription{sub(tooMany)}}, "subscriptions[0].patterns: 65 exceeds 64"},
		{"duplicate ID", configfile.File{Subscriptions: []configfile.Subscription{sub([]string{"urn:epc:idpat:sgtin:*.*.*"}), sub([]string{"urn:epc:idpat:sgtin:*.*.*"})}}, "duplicate subscription ID"},
		{"routes without destinations", configfile.File{Subscriptions: []configfile.Subscription{sub([]string{"urn:epc:idpat:sgtin:*.*.*"}, "erp")}}, `subscriptions[0].destinations[0]: unknown destination "erp"`},
		{"unknown route", configfile.File{Destinations: []configfile.Destination{dest}, Subscriptions: []configfile.Subscription{sub([]string{"urn:epc:idpat:sgtin:*.*.*"}, "crm")}}, `unknown destination "crm"`},
		{"credentials in URL", configfile.File{Destinations: []configfile.Destination{{Name: "erp", URL: "https://user:s3cret@erp.example/"}}}, "destinations[0].url: user information is not allowed"},
		{"unusable URL", configfile.File{Destinations: []configfile.Destination{{Name: "erp", URL: "ftp://erp.example/"}}}, "destinations[0].url"},
		{"bad destination name", configfile.File{Destinations: []configfile.Destination{{Name: "e r p", URL: "https://erp.example/"}}}, "destination name"},
		{"missing secret", configfile.File{Destinations: []configfile.Destination{{Name: "erp", URL: "https://erp.example/", Headers: map[string]configfile.SecretSource{"Authorization": {Env: "ERP_TOKEN"}}}}}, "destinations[0].headers.Authorization"},
		{"invalid header name", configfile.File{Destinations: []configfile.Destination{{Name: "erp", URL: "https://erp.example/", Headers: map[string]configfile.SecretSource{"Bad Header": {Env: "ERP_TOKEN"}}}}}, `"Bad Header" is not a valid header name`},
		{"bad report bounds", configfile.File{Report: configfile.Report{QueueCapacity: ptr(0)}, Destinations: []configfile.Destination{dest}}, "report:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.file.Version = configfile.Version
			_, err := Pipeline(tc.file, DefaultConfig(), noLookup, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Fatalf("error leaks a credential: %v", err)
			}
		})
	}
}

// TestPipelineRejectsHeaderInjection checks that a secret able to split a
// header line fails startup, and that the error does not carry the secret.
func TestPipelineRejectsHeaderInjection(t *testing.T) {
	for _, value := range []string{"t0ken\r\nX-Injected: 1", "t0ken\n", "t0\x00ken", "t0\x7fken"} {
		f := configfile.File{Version: configfile.Version, Destinations: []configfile.Destination{{
			Name: "erp", URL: "https://erp.example/",
			Headers: map[string]configfile.SecretSource{"Authorization": {Env: "ERP_TOKEN"}},
		}}}
		_, err := Pipeline(f, DefaultConfig(), func(string) (string, bool) { return value, true }, nil)
		if err == nil || !strings.Contains(err.Error(), "control characters") {
			t.Errorf("%q: err = %v", value, err)
		} else if strings.Contains(err.Error(), "t0") {
			t.Errorf("%q: error leaks the secret: %v", value, err)
		}
	}
	// A horizontal tab is legal inside a header value.
	f := configfile.File{Version: configfile.Version, Destinations: []configfile.Destination{{
		Name: "erp", URL: "https://erp.example/",
		Headers: map[string]configfile.SecretSource{"X-Token": {Env: "T"}},
	}}}
	if _, err := Pipeline(f, DefaultConfig(), func(string) (string, bool) { return "a\tb", true }, nil); err != nil {
		t.Fatalf("tab rejected: %v", err)
	}
}

// TestPipelineAdaptiveMode checks that adaptive mode builds an adaptive
// matcher, rejects out-of-bounds settings before starting, and reports its
// decisions in telemetry.
func TestPipelineAdaptiveMode(t *testing.T) {
	file := func(a configfile.FilterAdaptive) configfile.File {
		return configfile.File{
			Version:       configfile.Version,
			Filter:        configfile.Filter{Mode: ptr(configfile.ModeAdaptive), Adaptive: a},
			Subscriptions: []configfile.Subscription{{ID: "a", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}}},
		}
	}
	for name, a := range map[string]configfile.FilterAdaptive{
		"sampling too dense":               {SampleEvery: ptr(5)},
		"full shadow without experimental": {Shadow: ptr(configfile.ShadowFull)},
		"one candidate":                    {Candidates: []string{configfile.MatcherIndex}},
	} {
		_, err := Pipeline(file(a), DefaultConfig(), noLookup, nil)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "filter.adaptive" || !errors.Is(err, filter.ErrAdaptiveConfig) {
			t.Errorf("%s: err = %v, want a filter.adaptive field error", name, err)
		}
	}

	assembly, err := Pipeline(file(configfile.FilterAdaptive{SampleEvery: ptr(10), Warmup: ptr(1), Window: ptr(1)}), DefaultConfig(), noLookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%T", assembly.Subscriptions.current.Load().matcher); got != "*filter.Adaptive" {
		t.Fatalf("built %s, want *filter.Adaptive", got)
	}
	for range 20 {
		if err := assembly.Consumer.Consume(context.Background(), observation(t, "3074257bf7194e4000001a85", 0x3000, true)); err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(config(), dialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, ctx.Err() }), assembly.Consumer, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st := a.Status().Filter; st == nil || st.SampledCalls != 2 {
		t.Fatalf("status filter %+v, want two sampled calls", st)
	}
	metrics := httptest.NewRecorder()
	a.Handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"gosstrak_filter_adaptive_sampled_total 2\n", `gosstrak_filter_adaptive_selected{candidate="index"} `, `gosstrak_filter_adaptive_samples_total{candidate="linear"} `} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics lack %q:\n%s", want, metrics.Body.String())
		}
	}
}
