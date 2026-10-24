package filter_test

import (
	"fmt"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/filter"
)

// Adaptive benchmarks of the #7 slice-4 record. Overhead and regret come from
// the adaptive entry in BenchmarkWorkload; these cover convergence from the
// worse starting candidate and stability between equal candidates.
//
//	go test -run '^$' -bench 'Adaptive' -benchmem ./internal/filter/

var (
	indexCandidate  = filter.Candidate{Name: "index", Compile: filter.CompileIndex}
	linearCandidate = filter.Candidate{Name: "linear", Compile: filter.CompileLinear}
)

// BenchmarkAdaptiveConvergence starts on linear and reports how many calls,
// and how much matching time, pass before the controller selects the index.
// With the default policy that is at least warmup times sample_every calls.
func BenchmarkAdaptiveConvergence(b *testing.B) {
	for _, subs := range []int{100, 1000, 10000} {
		w := buildWorkload(b, subs, 0, 25, "sgtin")
		b.Run(fmt.Sprintf("subs=%d", subs), func(b *testing.B) {
			calls := 0
			buf := make([]filter.ID, 0, 8)
			for range b.N {
				m, err := filter.CompileAdaptive(w.subscriptions, filter.DefaultAdaptiveConfig(linearCandidate, indexCandidate))
				if err != nil {
					b.Fatal(err)
				}
				for i := 0; m.Stats().Selected != "index"; i++ {
					buf = m.Match(w.subjects[i%len(w.subjects)], buf[:0])
					calls++
					if calls > 10_000_000 {
						b.Fatal("never converged")
					}
				}
			}
			b.ReportMetric(float64(calls)/float64(b.N), "calls-to-switch")
		})
	}
}

// BenchmarkAdaptiveStability runs two identical index candidates, so every
// switch is a reaction to timing noise, and reports switches per million
// calls. Hysteresis and cooldown bound it; cooldown alone caps it at one per
// cooldown times sample_every calls.
func BenchmarkAdaptiveStability(b *testing.B) {
	w := buildWorkload(b, 1000, 10, 25, "mixed")
	twin := filter.Candidate{Name: "index-twin", Compile: filter.CompileIndex}
	for _, hysteresis := range []float64{0, filter.DefaultHysteresis} {
		b.Run(fmt.Sprintf("hysteresis=%g", hysteresis), func(b *testing.B) {
			cfg := filter.DefaultAdaptiveConfig(indexCandidate, twin)
			cfg.Hysteresis = hysteresis
			cfg.OverheadBudget = 1 // equal candidates; keep every shadow
			m, err := filter.CompileAdaptive(w.subscriptions, cfg)
			if err != nil {
				b.Fatal(err)
			}
			buf := make([]filter.ID, 0, 8)
			for range b.N {
				for _, s := range w.subjects {
					buf = m.Match(s, buf[:0])
				}
			}
			st := m.Stats()
			b.ReportMetric(float64(st.Switches)/float64(b.N*len(w.subjects))*1e6, "switches/Mcall")
		})
	}
}

// BenchmarkAdaptiveOverhead splits what adaptive mode costs over the single
// index: shadow-ns/tag is time spent running shadow candidates, and the rest
// of the difference is sampling bookkeeping, clock reads, and the controller.
func BenchmarkAdaptiveOverhead(b *testing.B) {
	for _, subs := range []int{100, 10000} {
		for _, residual := range []int{0, 50} {
			w := buildWorkload(b, subs, residual, 25, "sgtin")
			index, err := filter.CompileIndex(w.subscriptions)
			if err != nil {
				b.Fatal(err)
			}
			name := fmt.Sprintf("subs=%d/residual=%d", subs, residual)
			b.Run(name+"/matcher=index", func(b *testing.B) {
				buf := make([]filter.ID, 0, 8)
				for range b.N {
					for _, s := range w.subjects {
						buf = index.Match(s, buf[:0])
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(w.subjects)), "ns/tag")
			})
			b.Run(name+"/matcher=adaptive", func(b *testing.B) {
				m, err := filter.CompileAdaptive(w.subscriptions, filter.DefaultAdaptiveConfig(indexCandidate, linearCandidate))
				if err != nil {
					b.Fatal(err)
				}
				buf := make([]filter.ID, 0, 8)
				b.ResetTimer()
				for range b.N {
					for _, s := range w.subjects {
						buf = m.Match(s, buf[:0])
					}
				}
				calls := float64(b.N * len(w.subjects))
				st := m.Stats()
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/calls, "ns/tag")
				b.ReportMetric(float64(st.ShadowTime.Nanoseconds())/calls, "shadow-ns/tag")
			})
		}
	}
}
