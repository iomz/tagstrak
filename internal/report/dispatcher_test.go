package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a Reporter that records batches and returns scripted errors.
type recorder struct {
	mu      sync.Mutex
	batches []Batch
	errs    []error // returned in order; nil once exhausted
	block   bool    // block until ctx ends
	calls   int
}

func (r *recorder) Deliver(ctx context.Context, b Batch) error {
	r.mu.Lock()
	r.calls++
	block := r.block
	var err error
	if len(r.errs) > 0 {
		err, r.errs = r.errs[0], r.errs[1:]
	}
	if err == nil && !block {
		r.batches = append(r.batches, b)
	}
	r.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (r *recorder) ids() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for _, b := range r.batches {
		for _, e := range b.Events {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

func event(i int) Event {
	return Event{
		ID:            fmt.Sprintf("e%04d", i),
		Source:        "reader-1",
		ObservedAt:    time.Unix(int64(i), 0),
		Subscriptions: []string{"product"},
		Scheme:        "sgtin-96",
		EPC:           []byte{0x30, 0x74},
		Identity:      Identity{Authority: "gs1", KeyType: "sgtin", Components: map[string]string{"serial": fmt.Sprint(i)}},
	}
}

func testConfig() Config {
	c := DefaultConfig()
	c.QueueCapacity = 100
	c.BatchEvents = 10
	c.FlushInterval = time.Hour // tests flush through Close or size unless stated
	c.RequestTimeout = time.Second
	return c
}

func newDispatcher(t *testing.T, c Config, dests ...Destination) (*Dispatcher, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	d, err := New(c, dests, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Retries never wait in tests; the waits are recorded instead.
	d.sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	return d, &logs
}

func TestConfigValidation(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"zero queue":          func(c *Config) { c.QueueCapacity = 0 },
		"huge queue":          func(c *Config) { c.QueueCapacity = 2000000 },
		"batch over queue":    func(c *Config) { c.BatchEvents = c.QueueCapacity + 1 },
		"tiny batch bytes":    func(c *Config) { c.BatchBytes = 10 },
		"zero flush":          func(c *Config) { c.FlushInterval = 0 },
		"backoff max < base":  func(c *Config) { c.BackoffMax = c.BackoffBase / 2 },
		"zero attempts":       func(c *Config) { c.MaxAttempts = 0 },
		"unbounded budget":    func(c *Config) { c.RetryBudget = 2 * time.Hour },
		"zero request budget": func(c *Config) { c.RequestTimeout = 0 },
	} {
		c := DefaultConfig()
		mutate(&c)
		if err := c.Validate(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestNewRejectsBadDestinations(t *testing.T) {
	r := &recorder{}
	for name, dests := range map[string][]Destination{
		"empty name":   {{Name: "", Reporter: r}},
		"bad name":     {{Name: "a b", Reporter: r}},
		"url as name":  {{Name: "https://user:pw@host/", Reporter: r}},
		"nil reporter": {{Name: "a"}},
		"duplicate":    {{Name: "a", Reporter: r}, {Name: "a", Reporter: r}},
	} {
		if _, err := New(DefaultConfig(), dests, nil); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// TestDeliversInOrderInBoundedBatches covers the core contract: FIFO per
// destination, batches within BatchEvents, everything delivered by Close.
func TestDeliversInOrderInBoundedBatches(t *testing.T) {
	r := &recorder{}
	d, _ := newDispatcher(t, testConfig(), Destination{Name: "a", Reporter: r})
	d.Start()
	var want []string
	for i := range 35 {
		if !d.Enqueue("a", event(i)) {
			t.Fatalf("event %d not accepted", i)
		}
		want = append(want, event(i).ID)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := r.ids(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("delivery order %v", got)
	}
	for _, b := range r.batches {
		if len(b.Events) > 10 {
			t.Fatalf("batch of %d exceeds BatchEvents", len(b.Events))
		}
		var body struct {
			Schema string            `json:"schema"`
			Batch  string            `json:"batch"`
			Events []json.RawMessage `json:"events"`
		}
		if err := json.Unmarshal(b.JSON(), &body); err != nil {
			t.Fatalf("batch body is not JSON: %v", err)
		}
		if body.Schema != Schema || body.Batch != b.ID || len(body.Events) != len(b.Events) {
			t.Fatalf("batch body %+v does not match batch %s", body, b.ID)
		}
	}
	s := d.Stats()[0]
	if s.Enqueued != 35 || s.Delivered != 35 || s.Dropped+s.Failed+s.Undelivered != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestBatchBytesSplitsBatches(t *testing.T) {
	c := testConfig()
	c.BatchBytes = 1024
	r := &recorder{}
	d, _ := newDispatcher(t, c, Destination{Name: "a", Reporter: r})
	d.Start()
	for i := range 20 {
		d.Enqueue("a", event(i))
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.batches) < 2 {
		t.Fatalf("expected the byte budget to split batches, got %d", len(r.batches))
	}
	for _, b := range r.batches {
		if len(b.Events) > 1 && len(b.JSON()) > c.BatchBytes {
			t.Fatalf("batch of %d events is %d bytes", len(b.Events), len(b.JSON()))
		}
	}
	if len(r.ids()) != 20 {
		t.Fatalf("delivered %d events", len(r.ids()))
	}
}

func TestFlushIntervalSendsPartialBatch(t *testing.T) {
	c := testConfig()
	c.FlushInterval = 10 * time.Millisecond
	r := &recorder{}
	d, _ := newDispatcher(t, c, Destination{Name: "a", Reporter: r})
	d.Start()
	defer d.Close(context.Background())
	d.Enqueue("a", event(1))
	deadline := time.Now().Add(5 * time.Second)
	for len(r.ids()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("partial batch was not flushed")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFullQueueDropsNewest checks the overflow policy: Enqueue never blocks,
// and dropped events are counted.
func TestFullQueueDropsNewest(t *testing.T) {
	c := testConfig()
	c.QueueCapacity = 2
	c.BatchEvents = 2
	d, logs := newDispatcher(t, c, Destination{Name: "a", Reporter: &recorder{}})
	// Not started: nothing drains the queue.
	if !d.Enqueue("a", event(1)) || !d.Enqueue("a", event(2)) {
		t.Fatal("queue rejected events within capacity")
	}
	if d.Enqueue("a", event(3)) || d.Enqueue("a", event(4)) {
		t.Fatal("full queue accepted an event")
	}
	if s := d.Stats()[0]; s.Dropped != 2 || s.Enqueued != 2 {
		t.Fatalf("stats %+v", s)
	}
	if n := strings.Count(logs.String(), "dropping events"); n != 1 {
		t.Fatalf("overflow logged %d times, want once per episode", n)
	}
	// Nothing ran, so Close reports both queued events as undelivered.
	if err := d.Close(context.Background()); !errors.Is(err, ErrUndelivered) || !strings.Contains(err.Error(), "a=2") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnqueueRefusesUnknownAndClosed(t *testing.T) {
	d, _ := newDispatcher(t, testConfig(), Destination{Name: "a", Reporter: &recorder{}})
	d.Start()
	if d.Enqueue("nope", event(1)) {
		t.Fatal("unknown destination accepted an event")
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.Enqueue("a", event(1)) {
		t.Fatal("closed dispatcher accepted an event")
	}
	if s := d.Stats()[0]; s.Dropped != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestRetriesWithBoundedBackoff(t *testing.T) {
	c := testConfig()
	c.BackoffBase = 100 * time.Millisecond
	c.BackoffMax = 250 * time.Millisecond
	fail := errors.New("unavailable")
	r := &recorder{errs: []error{fail, fail, fail}}
	d, _ := newDispatcher(t, c, Destination{Name: "a", Reporter: r})
	var waits []time.Duration
	d.sleep = func(_ context.Context, w time.Duration) error { waits = append(waits, w); return nil }
	d.Start()
	d.Enqueue("a", event(1))
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond}
	if fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
	if s := d.Stats()[0]; s.Delivered != 1 || s.Retries != 3 {
		t.Fatalf("stats %+v", s)
	}
}

func TestRetryAfterIsHonouredUpToCap(t *testing.T) {
	c := testConfig()
	c.BackoffBase = 100 * time.Millisecond
	c.BackoffMax = time.Second
	r := &recorder{errs: []error{
		RetryAfter(errors.New("busy"), 700*time.Millisecond),
		RetryAfter(errors.New("busy"), time.Hour),
	}}
	d, _ := newDispatcher(t, c, Destination{Name: "a", Reporter: r})
	var waits []time.Duration
	d.sleep = func(_ context.Context, w time.Duration) error { waits = append(waits, w); return nil }
	d.Start()
	d.Enqueue("a", event(1))
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{700 * time.Millisecond, time.Second}; fmt.Sprint(waits) != fmt.Sprint(want) {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
}

// TestFinalFailuresAreCountedAndLogged covers every way a batch can finally
// fail. None of them may drop events silently.
func TestFinalFailuresAreCountedAndLogged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		errs   []error
		budget time.Duration
		reason string
		calls  int
	}{
		{"permanent", []error{Permanent(errors.New("bad request"))}, 30 * time.Second, "permanent error", 1},
		{"attempts", []error{errors.New("x"), errors.New("x"), errors.New("x")}, 30 * time.Second, "attempts exhausted", 3},
		{"budget", []error{errors.New("x"), errors.New("x"), errors.New("x")}, 150 * time.Millisecond, "retry budget exhausted", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig()
			c.MaxAttempts = 3
			c.BackoffBase = 200 * time.Millisecond
			c.RetryBudget = tc.budget
			r := &recorder{errs: tc.errs}
			d, logs := newDispatcher(t, c, Destination{Name: "a", Reporter: r})
			d.Start()
			d.Enqueue("a", event(1))
			d.Enqueue("a", event(2))
			if err := d.Close(context.Background()); err != nil {
				t.Fatalf("final failures are not shutdown losses: %v", err)
			}
			if s := d.Stats()[0]; s.Failed != 2 || s.Delivered != 0 {
				t.Fatalf("stats %+v", s)
			}
			if r.calls != tc.calls {
				t.Fatalf("attempts = %d, want %d", r.calls, tc.calls)
			}
			for _, want := range []string{"report delivery failed", "destination=a", "events=2", "first_event=e0001", "last_event=e0002", tc.reason} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("log lacks %q: %s", want, logs.String())
				}
			}
		})
	}
}

// TestCloseReportsUndeliveredAtDeadline covers a destination that never
// answers: Close must return by its deadline and account for every event.
func TestCloseReportsUndeliveredAtDeadline(t *testing.T) {
	c := testConfig()
	c.BatchEvents = 2
	r := &recorder{block: true}
	d, _ := newDispatcher(t, c, Destination{Name: "stuck", Reporter: r})
	d.Start()
	for i := range 5 {
		d.Enqueue("stuck", event(i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := d.Close(ctx)
	if !errors.Is(err, ErrUndelivered) || !strings.Contains(err.Error(), "stuck=5") {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close took %v past its deadline", elapsed)
	}
	if s := d.Stats()[0]; s.Undelivered != 5 || s.Delivered != 0 {
		t.Fatalf("stats %+v", s)
	}
}

// TestSlowDestinationDoesNotDelayOthers checks per-destination isolation.
func TestSlowDestinationDoesNotDelayOthers(t *testing.T) {
	stuck := &recorder{block: true}
	fast := &recorder{}
	c := testConfig()
	c.FlushInterval = 5 * time.Millisecond
	d, _ := newDispatcher(t, c, Destination{Name: "stuck", Reporter: stuck}, Destination{Name: "fast", Reporter: fast})
	d.Start()
	d.Enqueue("stuck", event(1))
	d.Enqueue("fast", event(2))
	deadline := time.Now().Add(5 * time.Second)
	for len(fast.ids()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("fast destination waited for the stuck one")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := d.Close(ctx); !errors.Is(err, ErrUndelivered) || !strings.Contains(err.Error(), "stuck=1") || strings.Contains(err.Error(), "fast") {
		t.Fatalf("err = %v", err)
	}
}

// TestConcurrentEnqueueAndClose is a race-detector test: enqueueing while
// closing must neither panic on a closed channel nor lose count.
func TestConcurrentEnqueueAndClose(t *testing.T) {
	r := &recorder{}
	d, _ := newDispatcher(t, testConfig(), Destination{Name: "a", Reporter: r})
	d.Start()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				d.Enqueue("a", event(g*1000+i))
			}
		}()
	}
	time.Sleep(time.Millisecond)
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	s := d.Stats()[0]
	if s.Enqueued+s.Dropped != 800 || s.Delivered != s.Enqueued {
		t.Fatalf("stats do not add up: %+v", s)
	}
}

func TestEncodedEventSchema(t *testing.T) {
	e := event(7)
	e.PureIdentityURI = "urn:epc:id:sgtin:0614141.812345.7"
	body, err := encodeEvent(e)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"id": "e0007", "source": "reader-1", "observedAt": "1970-01-01T00:00:07Z",
		"scheme": "sgtin-96", "epc": "3074", "pureIdentityURI": "urn:epc:id:sgtin:0614141.812345.7",
	} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	// ISO identities have no standards-backed text form; the field is omitted.
	iso := event(8)
	body, _ = encodeEvent(iso)
	if strings.Contains(string(body), "pureIdentityURI") {
		t.Fatalf("empty Pure Identity URI was encoded: %s", body)
	}
	// Absent subscriptions and components still encode as JSON containers.
	empty, _ := encodeEvent(Event{ID: "x"})
	if !strings.Contains(string(empty), `"subscriptions":[]`) || !strings.Contains(string(empty), `"components":{}`) {
		t.Fatalf("empty containers encoded as null: %s", empty)
	}
}
