// Package e2e_test runs the processes' application packages together over
// real sockets. It is test-only, so neither application package has to
// import the other.
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	golemu "github.com/iomz/tagstrak/v2/internal/app/golemu"
	gosstrak "github.com/iomz/tagstrak/v2/internal/app/gosstrak"
	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/control"
	"github.com/iomz/tagstrak/v2/internal/emulator"
	"github.com/iomz/tagstrak/v2/internal/report"
)

const (
	productEPC   = "3074257bf7194e4000001a85" // SGTIN-96 3.0614141.812345.6789
	unmatchedEPC = "3114257bf7194e4000001a85" // matched by no subscription
	productURI   = "urn:epc:id:sgtin:0614141.812345.6789"
)

func scenarioOf(epcs ...string) string {
	return `{"version":1,"seed":1,"repeat":true,"cycles":[["` + strings.Join(epcs, `","`) + `"]]}`
}

var productSubscription = configfile.Subscription{ID: "product", Patterns: []string{"urn:epc:idpat:sgtin:0614141.812345.*"}, Destinations: []string{"hook"}}

// receiver is an HTTP destination that records events and can fail or stall.
type receiver struct {
	mu       sync.Mutex
	events   []map[string]any
	failures atomic.Int32  // remaining requests to answer with 503
	stall    chan struct{} // when non-nil, requests wait until it is closed
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.stall != nil {
		select {
		case <-r.stall:
		case <-req.Context().Done():
			return
		}
	}
	if r.failures.Add(-1) >= 0 {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, _ := io.ReadAll(req.Body)
	var batch struct {
		Schema string           `json:"schema"`
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(body, &batch); err != nil || batch.Schema != report.Schema {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.events = append(r.events, batch.Events...)
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (r *receiver) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.events...)
}

func newDestination(t *testing.T, r *receiver) string {
	t.Helper()
	server := httptest.NewServer(r)
	t.Cleanup(func() {
		if r.stall != nil {
			select {
			case <-r.stall:
			default:
				close(r.stall)
			}
		}
		server.Close()
	})
	return server.URL + "/reports"
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// privateDir returns a short private directory for Unix sockets.
func privateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

type running struct {
	done chan error
	stop context.CancelFunc
}

// wait stops the component and returns its Run result.
func (r running) wait(t *testing.T) error {
	t.Helper()
	r.stop()
	select {
	case err := <-r.done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("component did not stop")
		return nil
	}
}

func run(t *testing.T, f func(context.Context) error) running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := running{done: make(chan error, 1), stop: cancel}
	go func() { r.done <- f(ctx) }()
	t.Cleanup(func() { r.stop() })
	return r
}

type emulatorOptions struct {
	scenario      string
	address       string
	controlSocket string
}

func startEmulator(t *testing.T, o emulatorOptions) (*golemu.App, running) {
	t.Helper()
	parsed, err := emulator.DecodeScenario(strings.NewReader(o.scenario))
	if err != nil {
		t.Fatal(err)
	}
	c := golemu.DefaultConfig()
	c.Emulator.Address = o.address
	if c.Emulator.Address == "" {
		c.Emulator.Address = "127.0.0.1:0"
	}
	c.Emulator.ReportInterval = 20 * time.Millisecond
	c.ControlSocket = o.controlSocket
	emu, err := golemu.New(c, parsed, emulator.SystemClock{}, emulator.SeededRandom, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := run(t, emu.Run)
	waitFor(t, "golemu to listen", func() bool { return emu.Status().Running && emu.Status().Address != "" })
	return emu, r
}

type middlewareOptions struct {
	reader        string
	destination   string
	file          configfile.File
	control       control.ListenConfig
	retries       int
	shutdown      time.Duration
	backoffBase   time.Duration
	flushInterval time.Duration
}

// startMiddleware builds gosstrak through the production configuration path:
// a configuration file value, Pipeline, then New.
func startMiddleware(t *testing.T, o middlewareOptions) (*gosstrak.App, gosstrak.Assembly, running) {
	t.Helper()
	f := o.file
	f.Version = configfile.Version
	if o.destination != "" {
		f.Destinations = []configfile.Destination{{Name: "hook", URL: o.destination}}
	}
	flush := configfile.Duration(20 * time.Millisecond)
	if o.flushInterval > 0 {
		flush = configfile.Duration(o.flushInterval)
	}
	base := configfile.Duration(time.Millisecond)
	if o.backoffBase > 0 {
		base = configfile.Duration(o.backoffBase)
	}
	f.Report.FlushInterval, f.Report.BackoffBase = &flush, &base

	c := gosstrak.DefaultConfig()
	c.Reader.Address = o.reader
	c.Reader.Source = "dock-1"
	c.Reader.MaxRetries = o.retries
	c.Reader.RetryMin = 10 * time.Millisecond
	c.Reader.RetryMax = 50 * time.Millisecond
	c.TelemetryAddress = "127.0.0.1:0"
	c.Control = o.control
	if o.shutdown > 0 {
		c.ShutdownTimeout = o.shutdown
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	assembly, err := gosstrak.Pipeline(f, c, func(string) (string, bool) { return "", false }, logger)
	if err != nil {
		t.Fatal(err)
	}
	app, err := gosstrak.New(c, &net.Dialer{}, assembly.Consumer, logger, assembly.Options...)
	if err != nil {
		t.Fatal(err)
	}
	return app, assembly, run(t, app.Run)
}

func checkProductEvents(t *testing.T, events []map[string]any) {
	t.Helper()
	ids := map[string]bool{}
	for _, e := range events {
		if e["pureIdentityURI"] != productURI || e["source"] != "dock-1" || e["scheme"] != "sgtin-96" {
			t.Fatalf("unexpected event %v", e)
		}
		if subs, _ := e["subscriptions"].([]any); len(subs) != 1 || subs[0] != "product" {
			t.Fatalf("subscriptions %v", e["subscriptions"])
		}
		id, _ := e["id"].(string)
		if ids[id] {
			t.Fatalf("event %s delivered twice", id)
		}
		ids[id] = true
	}
}

func cleanShutdown(t *testing.T, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestGolemuToHTTPDestination is the #9 end-to-end flow: golemu reports tags
// over LLRP, gosstrak translates and matches them, and the HTTP adapter
// delivers matching reports to a destination. Shutdown drains cleanly.
func TestGolemuToHTTPDestination(t *testing.T) {
	sink := &receiver{}
	emu, emuRun := startEmulator(t, emulatorOptions{scenario: scenarioOf(productEPC, unmatchedEPC)})
	app, _, appRun := startMiddleware(t, middlewareOptions{
		reader: emu.Status().Address, destination: newDestination(t, sink),
		file: configfile.File{Subscriptions: []configfile.Subscription{productSubscription}},
	})
	waitFor(t, "reports at the destination", func() bool { return len(sink.snapshot()) >= 3 })
	cleanShutdown(t, appRun.wait(t))
	cleanShutdown(t, emuRun.wait(t))

	events := sink.snapshot()
	checkProductEvents(t, events)
	stats := app.Status().Delivery
	if len(stats) != 1 || stats[0].Failed+stats[0].Dropped+stats[0].Undelivered != 0 {
		t.Fatalf("delivery stats %+v", stats)
	}
	// Everything accepted was delivered by the time shutdown returned.
	if stats[0].Delivered != stats[0].Enqueued || int(stats[0].Delivered) != len(events) {
		t.Fatalf("delivered %d of %d enqueued; destination saw %d", stats[0].Delivered, stats[0].Enqueued, len(events))
	}
}

// TestDestinationOutageThenRecovery checks that retries carry deliveries
// through an outage without loss.
func TestDestinationOutageThenRecovery(t *testing.T) {
	sink := &receiver{}
	sink.failures.Store(4)
	emu, emuRun := startEmulator(t, emulatorOptions{scenario: scenarioOf(productEPC)})
	app, _, appRun := startMiddleware(t, middlewareOptions{
		reader: emu.Status().Address, destination: newDestination(t, sink),
		file: configfile.File{Subscriptions: []configfile.Subscription{productSubscription}},
	})
	waitFor(t, "deliveries after the outage", func() bool { return len(sink.snapshot()) >= 3 })
	cleanShutdown(t, appRun.wait(t))
	cleanShutdown(t, emuRun.wait(t))

	checkProductEvents(t, sink.snapshot())
	s := app.Status().Delivery[0]
	if s.Retries < 4 || s.Failed+s.Dropped+s.Undelivered != 0 || int(s.Delivered) != len(sink.snapshot()) {
		t.Fatalf("stats %+v after %d events", s, len(sink.snapshot()))
	}
}

// TestReaderReconnect replaces the reader on the same address: gosstrak
// reconnects within its retry budget, ingestion resumes, and no event is
// delivered twice.
func TestReaderReconnect(t *testing.T) {
	sink := &receiver{}
	first, firstRun := startEmulator(t, emulatorOptions{scenario: scenarioOf(productEPC)})
	address := first.Status().Address
	app, _, appRun := startMiddleware(t, middlewareOptions{
		reader: address, destination: newDestination(t, sink), retries: 50,
		file: configfile.File{Subscriptions: []configfile.Subscription{productSubscription}},
	})
	waitFor(t, "deliveries from the first reader", func() bool { return len(sink.snapshot()) >= 2 })
	cleanShutdown(t, firstRun.wait(t))
	before := len(sink.snapshot())

	_, secondRun := startEmulator(t, emulatorOptions{scenario: scenarioOf(productEPC), address: address})
	waitFor(t, "deliveries from the second reader", func() bool { return len(sink.snapshot()) >= before+2 })
	cleanShutdown(t, appRun.wait(t))
	cleanShutdown(t, secondRun.wait(t))

	checkProductEvents(t, sink.snapshot())
	if status := app.Status(); status.Reader.Reconnects < 1 {
		t.Fatalf("reader reconnects %d", status.Reader.Reconnects)
	}
}

// TestScenarioReplacedThroughGolemuControl replaces golemu's scenario over its
// control socket; the newly reported tag reaches the destination.
func TestScenarioReplacedThroughGolemuControl(t *testing.T) {
	sink := &receiver{}
	socket := filepath.Join(privateDir(t), "golemu.sock")
	emu, emuRun := startEmulator(t, emulatorOptions{scenario: scenarioOf(unmatchedEPC), controlSocket: socket})
	_, _, appRun := startMiddleware(t, middlewareOptions{
		reader: emu.Status().Address, destination: newDestination(t, sink),
		file: configfile.File{Subscriptions: []configfile.Subscription{productSubscription}},
	})
	waitFor(t, "reports flowing", func() bool { return emu.Status().Reports >= 3 })
	if n := len(sink.snapshot()); n != 0 {
		t.Fatalf("%d events before the product tag existed", n)
	}

	client := unixClient(socket)
	req, _ := http.NewRequest(http.MethodPut, "http://golemu/scenario", strings.NewReader(scenarioOf(productEPC)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("scenario replacement status %d", resp.StatusCode)
	}
	waitFor(t, "the new tag at the destination", func() bool { return len(sink.snapshot()) >= 1 })
	cleanShutdown(t, appRun.wait(t))
	cleanShutdown(t, emuRun.wait(t))
	checkProductEvents(t, sink.snapshot())
}

// TestSubscriptionsReplacedThroughAPI starts gosstrak with API-managed,
// empty subscriptions and adds one over the management socket: routing
// changes live and the state file records it.
func TestSubscriptionsReplacedThroughAPI(t *testing.T) {
	sink := &receiver{}
	dir := privateDir(t)
	socket, state := filepath.Join(dir, "gosstrak.sock"), filepath.Join(dir, "subscriptions.json")
	source := configfile.SourceAPI
	emu, emuRun := startEmulator(t, emulatorOptions{scenario: scenarioOf(productEPC)})
	_, assembly, appRun := startMiddleware(t, middlewareOptions{
		reader: emu.Status().Address, destination: newDestination(t, sink),
		control: control.ListenConfig{Socket: socket},
		file:    configfile.File{Store: configfile.Store{Source: &source, StateFile: &state}},
	})
	waitFor(t, "reports flowing", func() bool { return emu.Status().Reports >= 3 })
	if n := len(sink.snapshot()); n != 0 {
		t.Fatalf("%d events with no subscriptions", n)
	}

	client := unixClient(socket)
	body := `{"subscriptions":[{"id":"product","patterns":["urn:epc:idpat:sgtin:0614141.812345.*"],"destinations":["hook"]}]}`
	req, _ := http.NewRequest(http.MethodPut, "http://gosstrak/v1/subscriptions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", `"0"`)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replacement status %d", resp.StatusCode)
	}
	waitFor(t, "deliveries under the new subscription", func() bool { return len(sink.snapshot()) >= 2 })
	cleanShutdown(t, appRun.wait(t))
	cleanShutdown(t, emuRun.wait(t))

	checkProductEvents(t, sink.snapshot())
	saved, err := configfile.LoadState(state)
	if err != nil || saved.Revision != 1 || len(saved.Subscriptions) != 1 || assembly.Subscriptions.Revision() != 1 {
		t.Fatalf("state %+v, %v; table revision %d", saved, err, assembly.Subscriptions.Revision())
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("control socket survived shutdown")
	}
}

// TestShutdownWithBatchesInFlight stops gosstrak while the destination never
// answers: Run returns by the shutdown deadline and reports the undelivered
// events instead of losing them silently.
func TestShutdownWithBatchesInFlight(t *testing.T) {
	sink := &receiver{stall: make(chan struct{})}
	emu, emuRun := startEmulator(t, emulatorOptions{scenario: scenarioOf(productEPC)})
	app, _, appRun := startMiddleware(t, middlewareOptions{
		reader: emu.Status().Address, destination: newDestination(t, sink), shutdown: 300 * time.Millisecond,
		file: configfile.File{Subscriptions: []configfile.Subscription{productSubscription}},
	})
	waitFor(t, "events enqueued", func() bool {
		d := app.Status().Delivery
		return len(d) == 1 && d[0].Enqueued >= 2
	})
	start := time.Now()
	err := appRun.wait(t)
	if !errors.Is(err, report.ErrUndelivered) {
		t.Fatalf("Run returned %v, want ErrUndelivered", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("shutdown took %v with a 300ms deadline", elapsed)
	}
	if s := app.Status().Delivery[0]; s.Undelivered == 0 || s.Delivered != 0 {
		t.Fatalf("stats %+v", s)
	}
	cleanShutdown(t, emuRun.wait(t))
}

func unixClient(socket string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
}
