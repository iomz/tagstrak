package report

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config bounds every queue, batch and retry. It is immutable after New.
type Config struct {
	// QueueCapacity is the number of events each destination can hold.
	QueueCapacity int
	// BatchEvents and BatchBytes close a batch, whichever is reached first.
	// An event larger than BatchBytes is sent in a batch of its own.
	BatchEvents int
	BatchBytes  int
	// FlushInterval sends a partial batch once its first event is this old.
	FlushInterval time.Duration
	// RequestTimeout bounds each delivery attempt.
	RequestTimeout time.Duration
	// MaxAttempts bounds attempts per batch, the first included.
	MaxAttempts int
	// BackoffBase doubles after each failed attempt, up to BackoffMax.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// RetryBudget bounds the time from a batch's first attempt to its last.
	RetryBudget time.Duration
}

// DefaultConfig returns the defaults from the #9 design record.
func DefaultConfig() Config {
	return Config{
		QueueCapacity:  10000,
		BatchEvents:    500,
		BatchBytes:     1 << 20,
		FlushInterval:  time.Second,
		RequestTimeout: 5 * time.Second,
		MaxAttempts:    5,
		BackoffBase:    200 * time.Millisecond,
		BackoffMax:     10 * time.Second,
		RetryBudget:    30 * time.Second,
	}
}

// Validate checks every bound, including upper limits, so a configuration
// cannot ask for unbounded memory or retrying.
func (c Config) Validate() error {
	switch {
	case c.QueueCapacity < 1 || c.QueueCapacity > 1000000:
		return fmt.Errorf("%w: queue capacity %d outside 1..1000000", ErrConfig, c.QueueCapacity)
	case c.BatchEvents < 1 || c.BatchEvents > c.QueueCapacity:
		return fmt.Errorf("%w: batch events %d outside 1..queue capacity", ErrConfig, c.BatchEvents)
	case c.BatchBytes < 1024 || c.BatchBytes > 64<<20:
		return fmt.Errorf("%w: batch bytes %d outside 1KiB..64MiB", ErrConfig, c.BatchBytes)
	case c.FlushInterval <= 0 || c.RequestTimeout <= 0 || c.BackoffBase <= 0:
		return fmt.Errorf("%w: flush interval, request timeout and backoff base must be positive", ErrConfig)
	case c.BackoffMax < c.BackoffBase:
		return fmt.Errorf("%w: backoff max below backoff base", ErrConfig)
	case c.MaxAttempts < 1 || c.MaxAttempts > 100:
		return fmt.Errorf("%w: max attempts %d outside 1..100", ErrConfig, c.MaxAttempts)
	case c.RetryBudget <= 0 || c.RetryBudget > time.Hour:
		return fmt.Errorf("%w: retry budget outside (0, 1h]", ErrConfig)
	}
	return nil
}

// Destination is a named delivery target. The name labels logs and metrics
// in place of the address, which may carry credentials.
type Destination struct {
	Name     string
	Reporter Reporter
}

var destinationName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// Stats is a snapshot of one destination's counters.
type Stats struct {
	Destination   string `json:"destination"`
	Enqueued      uint64 `json:"enqueued"`
	Delivered     uint64 `json:"delivered"`
	Dropped       uint64 `json:"dropped"`
	Failed        uint64 `json:"failed"`
	Undelivered   uint64 `json:"undelivered"`
	Retries       uint64 `json:"retries"`
	QueueDepth    int    `json:"queue_depth"`
	QueueCapacity int    `json:"queue_capacity"`
}

// Dispatcher owns one bounded queue and one worker per destination.
//
// New starts nothing. Start launches the workers, Enqueue hands events over
// without blocking, and Close drains within its context's deadline. The
// application owns that order, so delivery cannot outlive the process
// shutdown budget.
type Dispatcher struct {
	config   Config
	logger   *slog.Logger
	lanes    []*lane
	byName   map[string]*lane
	instance string
	batches  atomic.Uint64

	mu      sync.RWMutex
	started bool
	closed  bool

	abort   context.Context
	stop    context.CancelFunc
	workers sync.WaitGroup

	// sleep and now are test seams for deterministic backoff.
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

type lane struct {
	name     string
	reporter Reporter
	queue    chan Event
	dropping atomic.Bool

	enqueued    atomic.Uint64
	delivered   atomic.Uint64
	dropped     atomic.Uint64
	failed      atomic.Uint64
	undelivered atomic.Uint64
	retries     atomic.Uint64
}

// New validates the configuration and destinations. It performs no I/O and
// starts no goroutines.
func New(config Config, destinations []Destination, logger *slog.Logger) (*Dispatcher, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	d := &Dispatcher{
		config:   config,
		logger:   logger,
		byName:   make(map[string]*lane, len(destinations)),
		instance: NewInstanceID(),
		sleep:    sleepContext,
		now:      time.Now,
	}
	for _, dest := range destinations {
		if !destinationName.MatchString(dest.Name) {
			return nil, fmt.Errorf("%w: destination name %q must match %s", ErrConfig, dest.Name, destinationName)
		}
		if dest.Reporter == nil {
			return nil, fmt.Errorf("%w: destination %q has no reporter", ErrConfig, dest.Name)
		}
		if _, ok := d.byName[dest.Name]; ok {
			return nil, fmt.Errorf("%w: duplicate destination %q", ErrConfig, dest.Name)
		}
		l := &lane{name: dest.Name, reporter: dest.Reporter, queue: make(chan Event, config.QueueCapacity)}
		d.lanes = append(d.lanes, l)
		d.byName[dest.Name] = l
	}
	d.abort, d.stop = context.WithCancel(context.Background())
	return d, nil
}

// NewInstanceID returns a random identifier for one process run, so IDs built
// from it with a counter stay unique across restarts without persistence.
func NewInstanceID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail on supported platforms
	return hex.EncodeToString(b)
}

// Destinations returns the destination names in configuration order.
func (d *Dispatcher) Destinations() []string {
	names := make([]string, len(d.lanes))
	for i, l := range d.lanes {
		names[i] = l.name
	}
	return names
}

// Start launches one worker per destination. Calling it again has no effect.
func (d *Dispatcher) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.started || d.closed {
		return
	}
	d.started = true
	for _, l := range d.lanes {
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			d.run(l)
		}()
	}
}

// Enqueue hands an event to a destination's queue without blocking. It
// returns false when the event was not accepted: the destination is unknown,
// its queue is full, or the dispatcher is closed. A full or closed queue
// counts the event as dropped.
func (d *Dispatcher) Enqueue(destination string, e Event) bool {
	l, ok := d.byName[destination]
	if !ok {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		l.dropped.Add(1)
		return false
	}
	select {
	case l.queue <- e:
		l.enqueued.Add(1)
		l.dropping.Store(false)
		return true
	default:
		l.dropped.Add(1)
		// Log the start of an overflow episode, not every event in it.
		if l.dropping.CompareAndSwap(false, true) {
			d.logger.Warn("report queue full; dropping events", "destination", l.name, "capacity", cap(l.queue))
		}
		return false
	}
}

// Close stops accepting events and drains every queue until ctx ends. Work
// still queued or in flight at that point is abandoned and reported as
// ErrUndelivered with per-destination counts. Close waits for the workers
// before returning, so a Reporter that honours cancellation cannot leak.
func (d *Dispatcher) Close(ctx context.Context) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	started := d.started
	for _, l := range d.lanes {
		close(l.queue)
	}
	d.mu.Unlock()

	if started {
		done := make(chan struct{})
		go func() {
			d.workers.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			d.stop()
			<-done
		}
	}
	d.stop()

	var lost []string
	for _, l := range d.lanes {
		// Without workers nothing drained; count what never left the queue.
		for range l.queue {
			l.undelivered.Add(1)
		}
		if n := l.undelivered.Load(); n > 0 {
			lost = append(lost, fmt.Sprintf("%s=%d", l.name, n))
		}
	}
	if len(lost) > 0 {
		return fmt.Errorf("%w: %s", ErrUndelivered, strings.Join(lost, ", "))
	}
	return nil
}

// Stats returns a snapshot per destination, in configuration order.
func (d *Dispatcher) Stats() []Stats {
	out := make([]Stats, len(d.lanes))
	for i, l := range d.lanes {
		out[i] = Stats{
			Destination:   l.name,
			Enqueued:      l.enqueued.Load(),
			Delivered:     l.delivered.Load(),
			Dropped:       l.dropped.Load(),
			Failed:        l.failed.Load(),
			Undelivered:   l.undelivered.Load(),
			Retries:       l.retries.Load(),
			QueueDepth:    len(l.queue),
			QueueCapacity: cap(l.queue),
		}
	}
	return out
}

// run is one destination's worker. It builds batches in queue order and
// delivers each before taking the next, which is what keeps delivery FIFO.
// A batch closes on BatchEvents, BatchBytes or FlushInterval, whichever comes
// first; the flush timer runs only while a batch is open.
func (d *Dispatcher) run(l *lane) {
	envelope := batchEnvelope(d.instance + ":0000000000")
	var events []Event
	var encoded [][]byte
	size := envelope
	flush := time.NewTimer(d.config.FlushInterval)
	flush.Stop()
	defer flush.Stop()
	send := func() {
		if len(encoded) == 0 {
			return
		}
		flush.Stop()
		d.deliver(l, events, encoded)
		events, encoded, size = nil, nil, envelope
	}
	for {
		var due <-chan time.Time
		if len(encoded) > 0 {
			due = flush.C
		}
		select {
		case e, ok := <-l.queue:
			if !ok {
				send()
				return
			}
			body, err := encodeEvent(e)
			if err != nil {
				// Events are plain values; this would be a programming error.
				l.failed.Add(1)
				d.logger.Error("report event not encodable", "destination", l.name, "event", e.ID, "error", err)
				continue
			}
			if len(encoded) > 0 && size+len(body)+1 > d.config.BatchBytes {
				// Keep the byte budget; this event opens the next batch.
				send()
			}
			events = append(events, e)
			encoded = append(encoded, body)
			size += len(body) + 1
			if len(encoded) == 1 {
				flush.Reset(d.config.FlushInterval)
			}
			if len(encoded) >= d.config.BatchEvents || size >= d.config.BatchBytes {
				send()
			}
		case <-due:
			send()
		case <-d.abort.Done():
			d.abandon(l, len(encoded))
			return
		}
	}
}

// abandon counts in-hand and queued events as undelivered after the shutdown
// deadline. Close closes every queue before it can abort, so the range ends.
func (d *Dispatcher) abandon(l *lane, inHand int) {
	l.undelivered.Add(uint64(inHand))
	for range l.queue {
		l.undelivered.Add(1)
	}
}

// deliver sends one batch with bounded retries. It returns once the batch is
// delivered, finally failed, or abandoned at the shutdown deadline.
func (d *Dispatcher) deliver(l *lane, events []Event, encoded [][]byte) {
	id := fmt.Sprintf("%s:%010d", d.instance, d.batches.Add(1))
	batch := Batch{ID: id, Events: events, body: encodeBatch(id, encoded)}
	n := uint64(len(events))
	start := d.now()
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(d.abort, d.config.RequestTimeout)
		err := l.reporter.Deliver(ctx, batch)
		cancel()
		if err == nil {
			l.delivered.Add(n)
			return
		}
		if d.abort.Err() != nil {
			l.undelivered.Add(n)
			return
		}
		wait := d.backoff(attempt)
		if hint, ok := retryAfterHint(err); ok && hint > wait {
			wait = min(hint, d.config.BackoffMax)
		}
		final := ""
		switch {
		case IsPermanent(err):
			final = "permanent error"
		case attempt >= d.config.MaxAttempts:
			final = "attempts exhausted"
		case d.now().Sub(start)+wait > d.config.RetryBudget:
			final = "retry budget exhausted"
		}
		if final != "" {
			l.failed.Add(n)
			d.logger.Warn("report delivery failed",
				"destination", l.name, "batch", id, "events", n,
				"first_event", events[0].ID, "last_event", events[len(events)-1].ID,
				"attempts", attempt, "reason", final, "error", err)
			return
		}
		l.retries.Add(1)
		if d.sleep(d.abort, wait) != nil {
			l.undelivered.Add(n)
			return
		}
	}
}

// backoff returns the delay after a failed attempt: BackoffBase doubled per
// attempt, capped at BackoffMax. It is deterministic, like the reader's.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	wait := d.config.BackoffBase
	for i := 1; i < attempt && wait < d.config.BackoffMax; i++ {
		wait *= 2
	}
	return min(wait, d.config.BackoffMax)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
