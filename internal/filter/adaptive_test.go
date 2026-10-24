package filter_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
)

// fakeClock advances only when a scripted candidate runs.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// scripted is a candidate whose cost is set by the test and whose result
// names it, so a test can see which candidate answered.
type scripted struct {
	clock *fakeClock
	cost  *time.Duration
	id    filter.ID
	runs  *int
}

func (m scripted) Match(_ filter.Subject, dst []filter.ID) []filter.ID {
	m.clock.t = m.clock.t.Add(*m.cost)
	*m.runs++
	return append(dst, m.id)
}

func (m scripted) Len() int { return 1 }

type harness struct {
	clock *fakeClock
	costs map[string]*time.Duration
	runs  map[string]*int
	m     *filter.Adaptive
}

// newHarness builds an adaptive matcher over scripted candidates with the
// given initial costs in nanoseconds.
func newHarness(t *testing.T, cfg filter.AdaptiveConfig, costs map[string]time.Duration, names ...string) *harness {
	t.Helper()
	h := &harness{clock: &fakeClock{t: time.Unix(0, 0)}, costs: map[string]*time.Duration{}, runs: map[string]*int{}}
	for _, name := range names {
		cost, runs := costs[name], 0
		h.costs[name], h.runs[name] = &cost, &runs
		m := scripted{clock: h.clock, cost: h.costs[name], id: filter.ID(name), runs: h.runs[name]}
		cfg.Candidates = append(cfg.Candidates, filter.Candidate{Name: name, Compile: func([]filter.Subscription) (filter.Matcher, error) { return m, nil }})
	}
	cfg.Now = h.clock.now
	m, err := filter.CompileAdaptive(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.m = m
	return h
}

// call runs n matches and checks every result is the selected candidate's
// alone.
func (h *harness) call(t *testing.T, n int) {
	t.Helper()
	for range n {
		selected := h.m.Stats().Selected
		got := h.m.Match(filter.Subject{}, nil)
		if !slices.Equal(got, []filter.ID{filter.ID(selected)}) {
			t.Fatalf("result %v, want only the selected candidate %q", got, selected)
		}
	}
}

func (h *harness) selected() string { return h.m.Stats().Selected }

func TestAdaptiveSwitchesAfterWarmup(t *testing.T) {
	cfg := filter.AdaptiveConfig{SampleEvery: 10, Warmup: 3, Window: 3, Hysteresis: 0.1, Cooldown: 1}
	h := newHarness(t, cfg, map[string]time.Duration{"a": 100, "b": 50}, "a", "b")
	h.call(t, 29) // two sampled calls: b has only two samples
	if h.selected() != "a" {
		t.Fatalf("switched to %s before warmup", h.selected())
	}
	h.call(t, 1) // the third sample completes warmup
	if st := h.m.Stats(); st.Selected != "b" || st.Switches != 1 || st.SampledCalls != 3 {
		t.Fatalf("stats %+v, want b after one switch and three samples", st)
	}
	if st := h.m.Stats(); st.ShadowTime != 3*50 {
		t.Fatalf("shadow time %v, want three shadow runs of b", st.ShadowTime)
	}
}

func TestAdaptiveHysteresis(t *testing.T) {
	for _, tc := range []struct {
		cost    time.Duration
		switch_ bool
	}{
		{91, false}, // 9% cheaper
		{90, false}, // exactly the threshold: must be more than 10% cheaper
		{89, true},
	} {
		cfg := filter.AdaptiveConfig{SampleEvery: 10, Warmup: 1, Window: 1, Hysteresis: 0.1, Cooldown: 1}
		h := newHarness(t, cfg, map[string]time.Duration{"a": 100, "b": tc.cost}, "a", "b")
		h.call(t, 100)
		if got := h.selected() == "b"; got != tc.switch_ {
			t.Errorf("challenger at %d: switched %v, want %v", tc.cost, got, tc.switch_)
		}
	}
}

func TestAdaptiveCooldown(t *testing.T) {
	cfg := filter.AdaptiveConfig{SampleEvery: 10, Warmup: 1, Window: 1, Hysteresis: 0.1, Cooldown: 5}
	h := newHarness(t, cfg, map[string]time.Duration{"a": 100, "b": 50}, "a", "b")
	h.call(t, 10) // first sampled call switches to b
	if h.selected() != "b" {
		t.Fatalf("selected %s, want b", h.selected())
	}
	*h.costs["a"] = 10 // a becomes far cheaper at once
	h.call(t, 50)      // five more sampled calls: still within cooldown
	if h.selected() != "b" {
		t.Fatalf("switched back within cooldown")
	}
	h.call(t, 10) // the sixth leaves cooldown
	if st := h.m.Stats(); st.Selected != "a" || st.Switches != 2 {
		t.Fatalf("stats %+v, want a after two switches", st)
	}
}

func TestAdaptiveRoundRobinShadows(t *testing.T) {
	cfg := filter.AdaptiveConfig{SampleEvery: 10, Warmup: 1000, Window: 1}
	h := newHarness(t, cfg, map[string]time.Duration{"a": 1, "b": 1, "c": 1}, "a", "b", "c")
	h.call(t, 100) // ten sampled calls
	samples := map[string]uint64{}
	for _, c := range h.m.Stats().Candidates {
		samples[c.Name] = c.Samples
	}
	if samples["a"] != 10 || samples["b"] != 5 || samples["c"] != 5 {
		t.Fatalf("samples %v, want a 10 and b, c 5 each", samples)
	}
	if *h.runs["a"] != 100 || *h.runs["b"] != 5 || *h.runs["c"] != 5 {
		t.Fatalf("runs a=%d b=%d c=%d, want shadows only on sampled calls", *h.runs["a"], *h.runs["b"], *h.runs["c"])
	}
}

func TestAdaptiveFullShadow(t *testing.T) {
	cfg := filter.AdaptiveConfig{FullShadow: true, Experimental: true, Warmup: 1000, Window: 1}
	h := newHarness(t, cfg, map[string]time.Duration{"a": 1, "b": 1, "c": 1}, "a", "b", "c")
	h.call(t, 7)
	for _, name := range []string{"a", "b", "c"} {
		if *h.runs[name] != 7 {
			t.Fatalf("%s ran %d times, want every call", name, *h.runs[name])
		}
	}
}

// TestAdaptiveShadowNeverTouchesDst checks shadow results go to the
// matcher's own scratch, not into spare capacity of the caller's buffer.
func TestAdaptiveShadowNeverTouchesDst(t *testing.T) {
	cfg := filter.AdaptiveConfig{SampleEvery: 10, Warmup: 1000, Window: 1}
	h := newHarness(t, cfg, map[string]time.Duration{"a": 1, "b": 1}, "a", "b")
	buf := make([]filter.ID, 0, 4)
	full := buf[:4]
	for i := range full {
		full[i] = "sentinel"
	}
	for range 10 {
		buf = h.m.Match(filter.Subject{}, buf[:0])
	}
	if !slices.Equal(full, []filter.ID{"a", "sentinel", "sentinel", "sentinel"}) {
		t.Fatalf("caller's buffer %v, want shadow results kept out of it", full)
	}
}

func TestCompileAdaptiveRejectsBadConfig(t *testing.T) {
	two := []filter.Candidate{{Name: "index", Compile: filter.CompileIndex}, {Name: "linear", Compile: filter.CompileLinear}}
	five := append(append([]filter.Candidate{}, two...), filter.Candidate{Name: "c", Compile: filter.CompileLinear}, filter.Candidate{Name: "d", Compile: filter.CompileLinear}, filter.Candidate{Name: "e", Compile: filter.CompileLinear})
	for _, tc := range []struct {
		name string
		cfg  filter.AdaptiveConfig
	}{
		{"one candidate", filter.AdaptiveConfig{Candidates: two[:1]}},
		{"five candidates", filter.AdaptiveConfig{Candidates: five}},
		{"duplicate", filter.AdaptiveConfig{Candidates: []filter.Candidate{two[0], two[0]}}},
		{"unnamed", filter.AdaptiveConfig{Candidates: []filter.Candidate{two[0], {Name: "", Compile: filter.CompileLinear}}}},
		{"no compiler", filter.AdaptiveConfig{Candidates: []filter.Candidate{two[0], {Name: "x", Compile: nil}}}},
		{"full shadow without experimental", filter.AdaptiveConfig{Candidates: two, FullShadow: true}},
		{"sampling too dense", filter.AdaptiveConfig{Candidates: two, SampleEvery: 9}},
		{"window too large", filter.AdaptiveConfig{Candidates: two, Window: filter.MaxObservations + 1}},
		{"warmup too large", filter.AdaptiveConfig{Candidates: two, Warmup: filter.MaxObservations + 1}},
		{"negative window", filter.AdaptiveConfig{Candidates: two, Window: -1}},
		{"negative cooldown", filter.AdaptiveConfig{Candidates: two, Cooldown: -1}},
		{"hysteresis of one", filter.AdaptiveConfig{Candidates: two, Hysteresis: 1}},
		{"negative hysteresis", filter.AdaptiveConfig{Candidates: two, Hysteresis: -0.1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := filter.CompileAdaptive(nil, tc.cfg); !errors.Is(err, filter.ErrAdaptiveConfig) {
				t.Fatalf("err = %v, want ErrAdaptiveConfig", err)
			}
		})
	}
	if _, err := filter.CompileAdaptive([]filter.Subscription{{ID: ""}}, filter.AdaptiveConfig{Candidates: two}); !errors.Is(err, filter.ErrEmptyID) {
		t.Fatalf("subscriptions are not validated: %v", err)
	}
}

// TestAdaptiveConcurrentMatch runs real candidates from many goroutines, so
// sampled decisions race unsampled reads under the race detector.
func TestAdaptiveConcurrentMatch(t *testing.T) {
	subs := []filter.Subscription{
		{ID: "a", Patterns: []identity.Pattern{compilePattern(t, "urn:epc:idpat:sgtin:0614141.812345.*")}},
		{ID: "b", Patterns: []identity.Pattern{compilePattern(t, "urn:epc:pat:sgtin-96:3.0614141.*.*")}},
	}
	m, err := filter.CompileAdaptive(subs, filter.AdaptiveConfig{
		Candidates:  []filter.Candidate{{Name: "linear", Compile: filter.CompileLinear}, {Name: "index", Compile: filter.CompileIndex}},
		SampleEvery: 10, Warmup: 1, Window: 5, Cooldown: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	subject := subjectOf(translate(t, "urn:epc:tag:sgtin-96:3.0614141.812345.6789"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]filter.ID, 0, 2)
			for range 2000 {
				buf = m.Match(subject, buf[:0])
				if !slices.Equal(buf, []filter.ID{"a", "b"}) {
					t.Errorf("matched %v", buf)
					return
				}
			}
		}()
	}
	wg.Wait()
	if st := m.Stats(); st.SampledCalls != 1600 {
		t.Fatalf("sampled %d calls, want one in ten of 16000", st.SampledCalls)
	}
}
