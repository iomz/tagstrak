// Package e2e_test runs the processes' application packages together. It is
// test-only, so neither application package has to import the other.
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	golemu "github.com/iomz/tagstrak/v2/internal/app/golemu"
	gosstrak "github.com/iomz/tagstrak/v2/internal/app/gosstrak"
	"github.com/iomz/tagstrak/v2/internal/emulator"
	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/report"
	"github.com/iomz/tagstrak/v2/internal/report/httpreport"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// SGTIN-96 3.0614141.812345.6789, plus a tag no subscription matches.
const scenario = `{"version":1,"seed":1,"repeat":true,"cycles":[["3074257bf7194e4000001a85","3114257bf7194e4000001a85"]]}`

type receiver struct {
	mu     sync.Mutex
	events []map[string]any
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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

// TestGolemuToHTTPDestination is the #9 end-to-end flow: golemu reports tags
// over LLRP, gosstrak translates and matches them, and the HTTP adapter
// delivers matching reports to a destination. Shutdown drains cleanly.
func TestGolemuToHTTPDestination(t *testing.T) {
	sink := &receiver{}
	destination := httptest.NewServer(sink)
	defer destination.Close()

	// Reader emulator.
	parsed, err := emulator.DecodeScenario(strings.NewReader(scenario))
	if err != nil {
		t.Fatal(err)
	}
	emuConfig := golemu.DefaultConfig()
	emuConfig.Emulator.Address = "127.0.0.1:0"
	emuConfig.Emulator.ReportInterval = 20 * time.Millisecond
	emu, err := golemu.New(emuConfig, parsed, emulator.SystemClock{}, emulator.SeededRandom, nil)
	if err != nil {
		t.Fatal(err)
	}
	emuCtx, stopEmu := context.WithCancel(context.Background())
	defer stopEmu()
	emuDone := make(chan error, 1)
	go func() { emuDone <- emu.Run(emuCtx) }()
	waitFor(t, "golemu to listen", func() bool { return emu.Status().Running && emu.Status().Address != "" })

	// Delivery.
	hook, err := httpreport.New(destination.URL + "/reports")
	if err != nil {
		t.Fatal(err)
	}
	reportConfig := report.DefaultConfig()
	reportConfig.FlushInterval = 20 * time.Millisecond
	dispatcher, err := report.New(reportConfig, []report.Destination{{Name: "hook", Reporter: hook}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Matching.
	pattern, err := tdt.CompilePattern("urn:epc:idpat:sgtin:0614141.812345.*")
	if err != nil {
		t.Fatal(err)
	}
	var subscriptions filter.Set
	if err := subscriptions.Replace([]filter.Subscription{{ID: "product", Patterns: []identity.Pattern{pattern}}}); err != nil {
		t.Fatal(err)
	}
	processor, err := gosstrak.NewMatchingProcessor(&subscriptions, nil,
		gosstrak.DeliverTo(dispatcher, gosstrak.Routes{"product": {"hook"}}))
	if err != nil {
		t.Fatal(err)
	}

	// Middleware.
	config := gosstrak.DefaultConfig()
	config.Reader.Address = emu.Status().Address
	config.Reader.Source = "dock-1"
	config.TelemetryAddress = "127.0.0.1:0"
	consumer := gosstrak.NewTranslatingConsumer(tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1)), processor)
	app, err := gosstrak.New(config, &net.Dialer{}, consumer, nil, gosstrak.WithDelivery(dispatcher))
	if err != nil {
		t.Fatal(err)
	}
	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()
	appDone := make(chan error, 1)
	go func() { appDone <- app.Run(appCtx) }()

	waitFor(t, "reports at the destination", func() bool { return len(sink.snapshot()) >= 3 })
	stopApp()
	select {
	case err := <-appDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("gosstrak shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("gosstrak did not stop")
	}
	stopEmu()
	<-emuDone

	events := sink.snapshot()
	for _, e := range events {
		if e["pureIdentityURI"] != "urn:epc:id:sgtin:0614141.812345.6789" || e["source"] != "dock-1" || e["scheme"] != "sgtin-96" {
			t.Fatalf("unexpected event %v", e)
		}
		if subs, _ := e["subscriptions"].([]any); len(subs) != 1 || subs[0] != "product" {
			t.Fatalf("subscriptions %v", e["subscriptions"])
		}
	}
	stats := app.Status().Delivery
	if len(stats) != 1 || stats[0].Failed+stats[0].Dropped+stats[0].Undelivered != 0 {
		t.Fatalf("delivery stats %+v", stats)
	}
	// Everything accepted was delivered by the time shutdown returned.
	if stats[0].Delivered != stats[0].Enqueued || int(stats[0].Delivered) != len(events) {
		t.Fatalf("delivered %d of %d enqueued; destination saw %d", stats[0].Delivered, stats[0].Enqueued, len(events))
	}
}
