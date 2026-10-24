package filter_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
)

// differentialPatterns covers every shape the index treats differently:
// prefix-shaped encoding patterns at several depths, ranges that leave a hole
// in the mask, identity-level patterns, and other schemes.
var differentialPatterns = func() []string {
	var out []string
	for _, company := range []string{"0614141", "0614142"} {
		for _, item := range []string{"812345", "812346"} {
			out = append(out,
				"urn:epc:pat:sgtin-96:3."+company+"."+item+".6789",
				"urn:epc:pat:sgtin-96:3."+company+"."+item+".*",
				"urn:epc:pat:sgtin-96:[1-3]."+company+"."+item+".*",
				"urn:epc:pat:sgtin-96:3."+company+"."+item+".[1-6789]",
				"urn:epc:idpat:sgtin:"+company+"."+item+".6789",
				"urn:epc:idpat:sgtin:"+company+"."+item+".*",
			)
		}
		out = append(out,
			"urn:epc:pat:sgtin-96:3."+company+".*.*",
			"urn:epc:pat:sgtin-96:[0-7]."+company+".*.*",
			"urn:epc:idpat:sgtin:"+company+".*.*",
			"urn:epc:pat:sscc-96:0."+company+".*",
			"urn:epc:idpat:giai:"+company+".*",
		)
	}
	return append(out,
		"urn:epc:pat:sgtin-96:*.*.*.*",
		"urn:epc:idpat:sgtin:*.*.*",
		"urn:epc:pat:grai-96:0.000000000000..*",
		"urn:epc:idpat:grai:000000000000..*",
	)
}()

var differentialTags = func() []string {
	var out []string
	for _, filterValue := range []string{"0", "1", "3", "4", "7"} {
		for _, company := range []string{"0614141", "0614142"} {
			for _, item := range []string{"812345", "812346"} {
				for _, serial := range []string{"1", "6789", "6790"} {
					out = append(out, "urn:epc:tag:sgtin-96:"+filterValue+"."+company+"."+item+"."+serial)
				}
			}
		}
	}
	return append(out,
		"urn:epc:tag:sscc-96:0.0614141.1234567890",
		"urn:epc:tag:sscc-96:1.0614142.1234567890",
		"urn:epc:tag:giai-96:0.0614141.5678",
		"urn:epc:tag:grai-96:0.000000000000..17",
		"urn:epc:tag:grai-96:1.000000000000..17",
	)
}()

// differential compiles one subscription set with both matchers and fails if
// any tag gets a different answer. picks assigns patterns to subscriptions as
// (subscription, pattern) index pairs.
func differential(t *testing.T, patterns []identity.Pattern, subjects []filter.Subject, picks [][2]int) {
	t.Helper()
	bySub := map[int][]identity.Pattern{}
	for _, p := range picks {
		bySub[p[0]] = append(bySub[p[0]], patterns[p[1]])
	}
	var subs []filter.Subscription
	for i := range 8 {
		if ps := bySub[i]; len(ps) > 0 && len(ps) <= filter.MaxPatternsPerSubscription {
			subs = append(subs, filter.Subscription{ID: filter.ID(fmt.Sprint("s", i)), Patterns: ps})
		}
	}
	linear, err := filter.CompileLinear(subs)
	if err != nil {
		t.Fatal(err)
	}
	index, err := filter.CompileIndex(subs)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range subjects {
		want := linear.Match(s, nil)
		if got := index.Match(s, nil); !slices.Equal(got, want) {
			t.Fatalf("%s with %v: index %v, linear %v", differentialTags[i], picks, got, want)
		}
	}
}

func differentialInputs(t testing.TB) ([]identity.Pattern, []filter.Subject) {
	patterns := make([]identity.Pattern, len(differentialPatterns))
	for i, text := range differentialPatterns {
		patterns[i] = compilePattern(t, text)
	}
	subjects := make([]filter.Subject, len(differentialTags))
	for i, uri := range differentialTags {
		subjects[i] = subjectOf(translate(t, uri))
	}
	return patterns, subjects
}

// TestIndexAgreesWithLinear compares the matchers on fixed-seed random
// subscription sets, so every go test run covers far more than the fuzz
// seeds.
func TestIndexAgreesWithLinear(t *testing.T) {
	patterns, subjects := differentialInputs(t)
	r := rand.New(rand.NewPCG(7, 7))
	for range 2000 {
		picks := make([][2]int, 1+r.IntN(24))
		for i := range picks {
			picks[i] = [2]int{r.IntN(8), r.IntN(len(patterns))}
		}
		differential(t, patterns, subjects, picks)
	}
}

// FuzzIndexAgreesWithLinear reads byte pairs as (subscription, pattern)
// picks.
func FuzzIndexAgreesWithLinear(f *testing.F) {
	f.Add([]byte{0, 0})
	f.Add([]byte{0, 1, 0, 2, 1, 1, 7, 255})
	f.Add([]byte{3, 4, 2, 5, 1, 6, 0, 7, 3, 30, 2, 31})
	patterns, subjects := differentialInputs(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128 {
			data = data[:128]
		}
		picks := make([][2]int, 0, len(data)/2)
		for i := 0; i+1 < len(data); i += 2 {
			picks = append(picks, [2]int{int(data[i]) % 8, int(data[i+1]) % len(patterns)})
		}
		differential(t, patterns, subjects, picks)
	})
}
