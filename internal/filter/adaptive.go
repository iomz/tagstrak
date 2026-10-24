package filter

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Bounds on an adaptive configuration (#7 slice-3 design record, D4).
const (
	MaxCandidates      = 4
	MinSampleEvery     = 10
	MaxObservations    = 1000 // warmup and window, in samples per candidate
	DefaultSampleEvery = 100
	DefaultWarmup      = 20
	DefaultWindow      = 50
	DefaultHysteresis  = 0.10
	DefaultCooldown    = 500
	// DefaultOverheadBudget caps shadow time at 5% of the selected
	// candidate's estimated matching time once every candidate is warm.
	DefaultOverheadBudget = 0.05
)

// ErrAdaptiveConfig reports an adaptive configuration outside its bounds.
var ErrAdaptiveConfig = errors.New("filter: invalid adaptive configuration")

// Candidate is one matcher an adaptive matcher may select.
type Candidate struct {
	Name    string
	Compile Compiler
}

// AdaptiveConfig configures CompileAdaptive. Every field is used as given,
// so an explicit zero, such as no cooldown, is kept; start from
// DefaultAdaptiveConfig to get the defaults.
type AdaptiveConfig struct {
	// Candidates are compiled in order, one at a time; the first is selected
	// until the controller has evidence for another.
	Candidates []Candidate
	// SampleEvery makes every n-th Match also time one shadow candidate.
	SampleEvery int
	// Warmup is how many samples every candidate needs before any switch.
	Warmup int
	// Window is how many recent samples per candidate a decision compares.
	Window int
	// Hysteresis is how much cheaper, as a fraction, a challenger must be.
	Hysteresis float64
	// Cooldown is how many sampled calls must pass after a switch before
	// the next one.
	Cooldown int
	// OverheadBudget bounds shadow time, once every candidate has finished
	// warmup, to this fraction of the selected candidate's estimated total
	// matching time (its mean cost times calls). A sampled call whose shadow
	// would exceed it times only the selected candidate. Without it, sampling
	// a candidate a thousand times slower costs ten times the matching at a
	// 1% rate.
	OverheadBudget float64
	// FullShadow runs every candidate on every call. It multiplies matching
	// cost by the candidate count, so it requires Experimental.
	FullShadow   bool
	Experimental bool
	// Now is the clock that times samples; nil means time.Now. Tests inject
	// one to make decisions deterministic.
	Now func() time.Time
}

// DefaultAdaptiveConfig returns the default policy over the given
// candidates.
func DefaultAdaptiveConfig(candidates ...Candidate) AdaptiveConfig {
	return AdaptiveConfig{
		Candidates:     candidates,
		SampleEvery:    DefaultSampleEvery,
		Warmup:         DefaultWarmup,
		Window:         DefaultWindow,
		Hysteresis:     DefaultHysteresis,
		Cooldown:       DefaultCooldown,
		OverheadBudget: DefaultOverheadBudget,
	}
}

func (c AdaptiveConfig) validate() error {
	if n := len(c.Candidates); n < 2 || n > MaxCandidates {
		return fmt.Errorf("%w: %d candidates, want 2 to %d", ErrAdaptiveConfig, n, MaxCandidates)
	}
	seen := map[string]bool{}
	for i, cand := range c.Candidates {
		switch {
		case cand.Name == "":
			return fmt.Errorf("%w: candidate %d has no name", ErrAdaptiveConfig, i)
		case seen[cand.Name]:
			return fmt.Errorf("%w: duplicate candidate %q", ErrAdaptiveConfig, cand.Name)
		case cand.Compile == nil:
			return fmt.Errorf("%w: candidate %q has no compiler", ErrAdaptiveConfig, cand.Name)
		}
		seen[cand.Name] = true
	}
	if c.FullShadow && !c.Experimental {
		return fmt.Errorf("%w: full shadow execution requires experimental mode", ErrAdaptiveConfig)
	}
	if !c.FullShadow && c.SampleEvery < MinSampleEvery {
		return fmt.Errorf("%w: sample_every %d is below %d", ErrAdaptiveConfig, c.SampleEvery, MinSampleEvery)
	}
	for _, f := range []struct {
		name  string
		value int
	}{{"warmup", c.Warmup}, {"window", c.Window}} {
		if f.value < 1 || f.value > MaxObservations {
			return fmt.Errorf("%w: %s %d is outside 1 to %d", ErrAdaptiveConfig, f.name, f.value, MaxObservations)
		}
	}
	if c.Cooldown < 0 {
		return fmt.Errorf("%w: negative cooldown", ErrAdaptiveConfig)
	}
	// NaN fails every comparison, so it is rejected explicitly; TOML can
	// spell it.
	if math.IsNaN(c.Hysteresis) || c.Hysteresis < 0 || c.Hysteresis >= 1 {
		return fmt.Errorf("%w: hysteresis %g is outside [0, 1)", ErrAdaptiveConfig, c.Hysteresis)
	}
	if !c.FullShadow && (math.IsNaN(c.OverheadBudget) || c.OverheadBudget <= 0 || c.OverheadBudget > 1) {
		return fmt.Errorf("%w: overhead_budget %g is outside (0, 1]", ErrAdaptiveConfig, c.OverheadBudget)
	}
	return nil
}

// Compiler returns a Compiler that builds adaptive matchers with c.
func (c AdaptiveConfig) Compiler() Compiler {
	return func(subscriptions []Subscription) (Matcher, error) {
		m, err := CompileAdaptive(subscriptions, c)
		if err != nil {
			return nil, err
		}
		return m, nil
	}
}

// Adaptive answers every Match with its selected candidate and, on sampled
// calls, times a shadow candidate to decide whether to switch.
//
// Candidates agree on every subscription set, so which one answers changes
// cost, never results: an Adaptive honours the Matcher contract exactly as a
// single matcher does. It starts no goroutines. Unsampled calls read the
// selection atomically and take no lock; sampled calls are serialised.
type Adaptive struct {
	names    []string
	matchers []Matcher
	every    uint64
	full     bool
	budget   float64
	now      func() time.Time

	selected atomic.Int32
	calls    atomic.Uint64

	mu         sync.Mutex // guards the fields below
	ctl        controller
	nextShadow int
	scratch    []ID // receives shadow results, so they never touch dst
	sampled    uint64
	skipped    uint64 // sampled calls whose shadow the budget skipped
	shadowTime time.Duration
}

// CompileAdaptive validates subscriptions and cfg, then compiles every
// candidate.
func CompileAdaptive(subscriptions []Subscription, cfg AdaptiveConfig) (*Adaptive, error) {
	if err := validate(subscriptions); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &Adaptive{
		every:  uint64(cfg.SampleEvery),
		full:   cfg.FullShadow,
		budget: cfg.OverheadBudget,
		now:    cfg.Now,
		ctl:    newController(len(cfg.Candidates), cfg),
		// Sized so that typical shadow runs never allocate in Match.
		scratch: make([]ID, 0, 16),
	}
	for _, cand := range cfg.Candidates {
		m, err := cand.Compile(subscriptions)
		if err != nil {
			return nil, fmt.Errorf("candidate %q: %w", cand.Name, err)
		}
		a.names = append(a.names, cand.Name)
		a.matchers = append(a.matchers, m)
	}
	return a, nil
}

func (a *Adaptive) Match(s Subject, dst []ID) []ID {
	if !a.full && a.calls.Add(1)%a.every != 0 {
		return a.matchers[a.selected.Load()].Match(s, dst)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sel := a.ctl.selected
	shadow := -1
	if a.full || !a.ctl.warm() || a.withinBudget(sel) {
		shadow = a.pickShadow(sel)
	} else {
		a.skipped++
	}
	// Alternate which runs first, so neither side always gets the warm
	// cache for the subject.
	selectedFirst := a.sampled%2 == 0
	a.sampled++
	if selectedFirst {
		dst = a.timed(sel, s, dst, false)
	}
	for c := range a.matchers {
		if c != sel && (a.full || c == shadow) {
			a.scratch = a.timed(c, s, a.scratch[:0], true)
		}
	}
	if !selectedFirst {
		dst = a.timed(sel, s, dst, false)
	}
	if next, switched := a.ctl.decide(); switched {
		a.selected.Store(int32(next))
	}
	return dst
}

func (a *Adaptive) timed(c int, s Subject, dst []ID, shadow bool) []ID {
	start := a.now()
	dst = a.matchers[c].Match(s, dst)
	cost := a.now().Sub(start)
	a.ctl.observe(c, cost)
	if shadow {
		a.shadowTime += cost
	}
	return dst
}

// withinBudget reports whether shadow time so far is within the budget of
// the selected candidate's estimated matching time.
func (a *Adaptive) withinBudget(sel int) bool {
	estimated := float64(a.ctl.mean(sel)) * float64(a.calls.Load())
	return float64(a.shadowTime) <= a.budget*estimated
}

// pickShadow returns the next non-selected candidate, round-robin. Under
// full shadow every non-selected candidate runs instead.
func (a *Adaptive) pickShadow(sel int) int {
	n := len(a.matchers)
	a.nextShadow = (a.nextShadow + 1) % n
	if a.nextShadow == sel {
		a.nextShadow = (a.nextShadow + 1) % n
	}
	return a.nextShadow
}

func (a *Adaptive) Len() int { return a.matchers[0].Len() }

// AdaptiveStats is a snapshot of an adaptive matcher's decisions. It covers
// one compiled matcher, so replacing subscriptions starts it over.
type AdaptiveStats struct {
	Selected     string
	Switches     uint64
	SampledCalls uint64
	// SkippedShadows counts sampled calls that timed only the selected
	// candidate because a shadow would have exceeded the overhead budget.
	SkippedShadows uint64
	// ShadowTime is the matching time spent on shadow candidates, the cost
	// of adaptivity beyond clock reads.
	ShadowTime time.Duration
	Candidates []CandidateStats
}

// CandidateStats describes one candidate's observed cost.
type CandidateStats struct {
	Name    string
	Samples uint64
	// MeanCost is the mean over the decision window, zero before any sample.
	MeanCost time.Duration
}

// Stats returns the current decision state.
func (a *Adaptive) Stats() AdaptiveStats {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := AdaptiveStats{
		Selected:       a.names[a.ctl.selected],
		Switches:       a.ctl.switches,
		SampledCalls:   a.sampled,
		SkippedShadows: a.skipped,
		ShadowTime:     a.shadowTime,
	}
	for c, name := range a.names {
		st.Candidates = append(st.Candidates, CandidateStats{Name: name, Samples: a.ctl.counts[c], MeanCost: a.ctl.mean(c)})
	}
	return st
}

// controller holds the switching policy. It has no clock and no locking of
// its own: costs come in through observe, and decide is a pure function of
// the fields, which is what makes decisions deterministic under test.
type controller struct {
	warmup, cooldown int
	hysteresis       float64

	selected    int
	switches    uint64
	sinceSwitch int // decisions since the last switch
	counts      []uint64
	rings       [][]time.Duration // the last window costs per candidate
	next        []int             // ring write positions
}

func newController(n int, cfg AdaptiveConfig) controller {
	c := controller{
		warmup:     cfg.Warmup,
		cooldown:   cfg.Cooldown,
		hysteresis: cfg.Hysteresis,
		counts:     make([]uint64, n),
		rings:      make([][]time.Duration, n),
		next:       make([]int, n),
	}
	for i := range c.rings {
		c.rings[i] = make([]time.Duration, 0, cfg.Window)
	}
	return c
}

func (c *controller) observe(candidate int, cost time.Duration) {
	c.counts[candidate]++
	ring := c.rings[candidate]
	if len(ring) < cap(ring) {
		c.rings[candidate] = append(ring, cost)
		return
	}
	ring[c.next[candidate]] = cost
	c.next[candidate] = (c.next[candidate] + 1) % len(ring)
}

// warm reports whether every candidate has finished warmup.
func (c *controller) warm() bool {
	for _, n := range c.counts {
		if n < uint64(c.warmup) {
			return false
		}
	}
	return true
}

func (c *controller) mean(candidate int) time.Duration {
	ring := c.rings[candidate]
	if len(ring) == 0 {
		return 0
	}
	var sum time.Duration
	for _, d := range ring {
		sum += d
	}
	return sum / time.Duration(len(ring))
}

// decide runs once per sampled call. It switches to the cheapest candidate
// when every candidate is past warmup, no switch happened within cooldown,
// and the cheapest beats the selected one by more than the hysteresis.
func (c *controller) decide() (int, bool) {
	c.sinceSwitch++
	if !c.warm() {
		return c.selected, false
	}
	if c.switches > 0 && c.sinceSwitch <= c.cooldown {
		return c.selected, false
	}
	best := c.selected
	for cand := range c.counts {
		if c.mean(cand) < c.mean(best) {
			best = cand
		}
	}
	if best == c.selected || float64(c.mean(best)) >= float64(c.mean(c.selected))*(1-c.hysteresis) {
		return c.selected, false
	}
	c.selected = best
	c.switches++
	c.sinceSwitch = 0
	return best, true
}
