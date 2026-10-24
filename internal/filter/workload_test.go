package filter_test

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// The workload suite of the #7 slice-2 design record. No field subscription
// sets or tag traces exist, so these are synthetic stand-ins that cover the
// pattern shapes the matchers treat differently, not measured workloads.
//
//	go test -run '^$' -bench 'Workload|Compile' -benchmem ./internal/filter/

var (
	workloadSizes    = []int{10, 100, 1000, 10000}
	workloadResidual = []int{0, 10, 50} // percent of subscriptions using a filter range
	workloadHits     = []int{0, 1, 25}  // percent of tags matching some subscription
	workloadMixes    = []string{"sgtin", "mixed"}
	workloadMatchers = []struct {
		name    string
		compile filter.Compiler
	}{
		{"linear", filter.CompileLinear},
		{"index", filter.CompileIndex},
		// Default policy, index selected first; regret is measured against
		// the better of the two single matchers on each workload.
		{"adaptive", filter.DefaultAdaptiveConfig(
			filter.Candidate{Name: "index", Compile: filter.CompileIndex},
			filter.Candidate{Name: "linear", Compile: filter.CompileLinear},
		).Compiler()},
	}
)

const workloadTags = 1000

type product struct {
	scheme       string // sgtin, sscc or grai
	company, ref string
}

// subscriptionPattern renders one subscription for a product. Half are
// encoding-level and half identity-level, both prefix-shaped; residual ones
// constrain the filter value with a range, which leaves a hole in the mask.
func subscriptionPattern(p product, i int, residual bool) string {
	identityLevel := i%2 == 1 && !residual
	switch p.scheme {
	case "sscc":
		switch {
		case residual:
			return "urn:epc:pat:sscc-96:[0-1]." + p.company + ".*"
		case identityLevel:
			return "urn:epc:idpat:sscc:" + p.company + ".*"
		}
		return "urn:epc:pat:sscc-96:0." + p.company + ".*"
	case "grai":
		switch {
		case residual:
			return "urn:epc:pat:grai-96:[0-1]." + p.company + "." + p.ref + ".*"
		case identityLevel:
			return "urn:epc:idpat:grai:" + p.company + "." + p.ref + ".*"
		}
		return "urn:epc:pat:grai-96:0." + p.company + "." + p.ref + ".*"
	}
	switch {
	case residual:
		return "urn:epc:pat:sgtin-96:[1-3]." + p.company + "." + p.ref + ".*"
	case identityLevel:
		return "urn:epc:idpat:sgtin:" + p.company + "." + p.ref + ".*"
	}
	return "urn:epc:pat:sgtin-96:3." + p.company + "." + p.ref + ".*"
}

func tagURI(p product, serial int) string {
	switch p.scheme {
	case "sscc":
		return fmt.Sprintf("urn:epc:tag:sscc-96:0.%s.%010d", p.company, serial)
	case "grai":
		return fmt.Sprintf("urn:epc:tag:grai-96:0.%s.%s.%d", p.company, p.ref, serial)
	}
	return fmt.Sprintf("urn:epc:tag:sgtin-96:3.%s.%s.%d", p.company, p.ref, serial)
}

type workload struct {
	subscriptions []filter.Subscription
	translations  []tdt.Translation // kept alive for the subjects' views
	subjects      []filter.Subject
}

// buildWorkload is deterministic for a given configuration.
func buildWorkload(tb testing.TB, subs, residualPct, hitPct int, mix string) workload {
	tb.Helper()
	r := rand.New(rand.NewPCG(uint64(subs), uint64(residualPct*100+hitPct)))
	schemes := []string{"sgtin"}
	if mix == "mixed" {
		schemes = []string{"sgtin", "sscc", "grai"}
	}
	seen := map[product]bool{}
	newProduct := func() product {
		for {
			p := product{scheme: schemes[r.IntN(len(schemes))], company: fmt.Sprintf("%07d", r.IntN(10000000))}
			switch p.scheme {
			case "sgtin":
				p.ref = fmt.Sprintf("%06d", r.IntN(1000000))
			case "grai":
				p.ref = fmt.Sprintf("%05d", r.IntN(100000))
			}
			if !seen[p] {
				seen[p] = true
				return p
			}
		}
	}
	var w workload
	subscribed := make([]product, subs)
	for i := range subscribed {
		subscribed[i] = newProduct()
		residual := i*100 < residualPct*subs
		w.subscriptions = append(w.subscriptions, filter.Subscription{
			ID:       filter.ID(fmt.Sprintf("s%05d", i)),
			Patterns: []identity.Pattern{compilePattern(tb, subscriptionPattern(subscribed[i], i, residual))},
		})
	}
	// Residual subscriptions are spread through the list, not grouped first.
	r.Shuffle(len(w.subscriptions), func(i, j int) {
		w.subscriptions[i], w.subscriptions[j] = w.subscriptions[j], w.subscriptions[i]
	})
	for i := range workloadTags {
		p := newProduct() // unsubscribed
		if r.IntN(100) < hitPct {
			p = subscribed[r.IntN(subs)]
		}
		w.translations = append(w.translations, translate(tb, tagURI(p, i+1)))
	}
	for _, tr := range w.translations {
		w.subjects = append(w.subjects, subjectOf(tr))
	}
	return w
}

// BenchmarkWorkload matches 1,000 tags per op and reports ns/tag.
func BenchmarkWorkload(b *testing.B) {
	for _, mix := range workloadMixes {
		for _, subs := range workloadSizes {
			for _, residual := range workloadResidual {
				for _, hits := range workloadHits {
					w := buildWorkload(b, subs, residual, hits, mix)
					for _, m := range workloadMatchers {
						matcher, err := m.compile(w.subscriptions)
						if err != nil {
							b.Fatal(err)
						}
						name := fmt.Sprintf("mix=%s/subs=%d/residual=%d/hits=%d/matcher=%s", mix, subs, residual, hits, m.name)
						b.Run(name, func(b *testing.B) {
							buf := make([]filter.ID, 0, 8)
							matched := 0
							b.ReportAllocs()
							for range b.N {
								matched = 0
								for _, s := range w.subjects {
									buf = matcher.Match(s, buf[:0])
									matched += len(buf)
								}
							}
							b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(w.subjects)), "ns/tag")
							b.ReportMetric(float64(matched), "matches/op")
						})
					}
				}
			}
		}
	}
}

// BenchmarkCompile measures what Replace pays per update, and how much memory
// the compiled matcher retains.
func BenchmarkCompile(b *testing.B) {
	for _, subs := range workloadSizes {
		w := buildWorkload(b, subs, 10, 0, "mixed")
		for _, m := range workloadMatchers {
			b.Run(fmt.Sprintf("subs=%d/matcher=%s", subs, m.name), func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					if _, err := m.compile(w.subscriptions); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				var before, after runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&before)
				matcher, _ := m.compile(w.subscriptions)
				runtime.GC()
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(matcher)
				b.ReportMetric(float64(after.HeapAlloc)-float64(before.HeapAlloc), "retained-B")
			})
		}
	}
}

// TestWorkloadMatchersAgree keeps the benchmark inputs honest: on every
// workload both matchers return the same matches.
func TestWorkloadMatchersAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds every workload")
	}
	for _, mix := range workloadMixes {
		for _, subs := range []int{10, 1000} {
			for _, residual := range workloadResidual {
				w := buildWorkload(t, subs, residual, 25, mix)
				linear, err := filter.CompileLinear(w.subscriptions)
				if err != nil {
					t.Fatal(err)
				}
				index, err := filter.CompileIndex(w.subscriptions)
				if err != nil {
					t.Fatal(err)
				}
				matched := 0
				for i, s := range w.subjects {
					want := linear.Match(s, nil)
					got := index.Match(s, nil)
					if fmt.Sprint(got) != fmt.Sprint(want) {
						t.Fatalf("mix=%s subs=%d residual=%d tag %d: index %v, linear %v", mix, subs, residual, i, got, want)
					}
					matched += len(want)
				}
				if matched == 0 {
					t.Fatalf("mix=%s subs=%d residual=%d: no tag matched; the workload does not exercise matching", mix, subs, residual)
				}
			}
		}
	}
}
