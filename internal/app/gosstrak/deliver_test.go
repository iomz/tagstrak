package gosstrak

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/report"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// fakeDelivery records enqueued events and lifecycle calls.
type fakeDelivery struct {
	mu       sync.Mutex
	names    []string
	events   map[string][]report.Event
	started  bool
	closeErr error
	closed   bool
}

func newFakeDelivery(names ...string) *fakeDelivery {
	return &fakeDelivery{names: names, events: map[string][]report.Event{}}
}

func (f *fakeDelivery) Destinations() []string { return f.names }
func (f *fakeDelivery) Start()                 { f.mu.Lock(); f.started = true; f.mu.Unlock() }
func (f *fakeDelivery) Enqueue(name string, e report.Event) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[name] = append(f.events[name], e)
	return true
}
func (f *fakeDelivery) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}
func (f *fakeDelivery) Stats() []report.Stats {
	return []report.Stats{{Destination: "hook", Delivered: 3, QueueCapacity: 10}}
}

func mustPattern(t *testing.T, text string) identity.Pattern {
	t.Helper()
	p, err := tdt.CompilePattern(text)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEventOfRendersStandardsBackedValues(t *testing.T) {
	obs := observation(t, "3074257bf7194e4000001a85", 0x3000, true)
	tr, err := tdt.New().Translate(TranslationInput(obs))
	if err != nil {
		t.Fatal(err)
	}
	e := EventOf("i:1", obs, tr)
	if e.ID != "i:1" || e.Source != "test" || !e.ObservedAt.Equal(time.Unix(1, 0)) || e.Scheme != "sgtin-96" {
		t.Fatalf("event %+v", e)
	}
	if e.PureIdentityURI != "urn:epc:id:sgtin:0614141.812345.6789" {
		t.Fatalf("pure identity URI %q", e.PureIdentityURI)
	}
	if e.Identity.Authority != "gs1" || e.Identity.KeyType != "sgtin" || e.Identity.Components["serial"] != "6789" {
		t.Fatalf("identity %+v", e.Identity)
	}
	// The event holds its own copy of the EPC, because it outlives the
	// observation in a delivery queue.
	e.EPC[0] = 0xff
	if obs.Tag().EPC()[0] != 0x30 {
		t.Fatal("event shares EPC memory with the observation")
	}
}

func TestDeliverToRejectsUnknownDestinations(t *testing.T) {
	if _, err := NewMatchingProcessor(nil, nil, DeliverTo(nil)); err == nil {
		t.Fatal("nil delivery accepted")
	}
	// The table may route to "nope", which delivery lacks.
	_, err := NewMatchingProcessor(NewSubscriptionTable([]string{"nope"}), nil, DeliverTo(newFakeDelivery("hook")))
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("err = %v", err)
	}
}

// table builds a subscription table, failing the test on invalid input.
func table(t *testing.T, destinations []string, spec ...configfile.Subscription) *SubscriptionTable {
	t.Helper()
	tbl := NewSubscriptionTable(destinations)
	if err := tbl.Load(spec, 1); err != nil {
		t.Fatal(err)
	}
	return tbl
}

// TestProcessorRoutesMatchesByDestination checks grouping: one event per
// destination, carrying that destination's subscriptions in match order, and
// one shared observation ID.
func TestProcessorRoutesMatchesByDestination(t *testing.T) {
	tbl := table(t, []string{"erp", "audit"},
		configfile.Subscription{ID: "product", Patterns: []string{"urn:epc:idpat:sgtin:0614141.812345.*"}, Destinations: []string{"erp"}},
		configfile.Subscription{ID: "company", Patterns: []string{"urn:epc:idpat:sgtin:0614141.*.*"}, Destinations: []string{"erp", "audit"}},
		configfile.Subscription{ID: "filter3", Patterns: []string{"urn:epc:pat:sgtin-96:3.0614141.812345.*"}, Destinations: []string{"audit"}},
		configfile.Subscription{ID: "unrouted", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}},
	)
	delivery := newFakeDelivery("erp", "audit")
	p, err := NewMatchingProcessor(tbl, nil, DeliverTo(delivery))
	if err != nil {
		t.Fatal(err)
	}
	consumer := NewTranslatingConsumer(tdt.New(), p)
	for range 2 {
		if err := consumer.Consume(context.Background(), observation(t, "3074257bf7194e4000001a85", 0x3000, true)); err != nil {
			t.Fatal(err)
		}
	}
	erp, audit := delivery.events["erp"], delivery.events["audit"]
	if len(erp) != 2 || len(audit) != 2 {
		t.Fatalf("erp %d events, audit %d events", len(erp), len(audit))
	}
	if got := fmt.Sprint(erp[0].Subscriptions); got != "[product company]" {
		t.Errorf("erp subscriptions %s", got)
	}
	if got := fmt.Sprint(audit[0].Subscriptions); got != "[company filter3]" {
		t.Errorf("audit subscriptions %s", got)
	}
	if erp[0].ID != audit[0].ID || erp[0].ID == erp[1].ID {
		t.Errorf("IDs: erp %s/%s, audit %s", erp[0].ID, erp[1].ID, audit[0].ID)
	}
}

func TestProcessorWithoutRoutedMatchesEnqueuesNothing(t *testing.T) {
	tbl := table(t, []string{"hook"}, configfile.Subscription{ID: "pallets", Patterns: []string{"urn:epc:idpat:sscc:*.*"}, Destinations: []string{"hook"}})
	delivery := newFakeDelivery("hook")
	p, err := NewMatchingProcessor(tbl, nil, DeliverTo(delivery))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewTranslatingConsumer(tdt.New(), p).Consume(context.Background(), observation(t, "3074257bf7194e4000001a85", 0x3000, true)); err != nil {
		t.Fatal(err)
	}
	if len(delivery.events) != 0 {
		t.Fatalf("unmatched tag was delivered: %v", delivery.events)
	}
}

// TestAppOwnsDeliveryLifecycle checks that Run starts delivery, closes it
// after the drain, reports its stats, and returns its undelivered error.
func TestAppOwnsDeliveryLifecycle(t *testing.T) {
	delivery := newFakeDelivery("hook")
	delivery.closeErr = report.ErrUndelivered
	consumer := consumerFunc(func(context.Context, inventory.Observation) error { return nil })
	a, err := New(config(), dialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}), consumer, nil, WithDelivery(delivery))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	eventually(t, func() bool { delivery.mu.Lock(); defer delivery.mu.Unlock(); return delivery.started })
	if got := a.Status().Delivery; len(got) != 1 || got[0].Delivered != 3 {
		t.Fatalf("status delivery %+v", got)
	}
	metrics := httptest.NewRecorder()
	a.Handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{`gosstrak_report_delivered_total{destination="hook"} 3`, `gosstrak_report_queue_capacity{destination="hook"} 10`} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics lack %q:\n%s", want, metrics.Body.String())
		}
	}
	cancel()
	err = await(t, done)
	if !errors.Is(err, report.ErrUndelivered) {
		t.Fatalf("Run result %v does not report undelivered work", err)
	}
	if !delivery.closed {
		t.Fatal("delivery was not closed")
	}
}
