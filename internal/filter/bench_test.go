package filter_test

import (
	"fmt"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
)

// BenchmarkLinearMatch records the single-mode baseline that later #7
// candidates are compared against. Half the subscriptions are identity-level
// and half encoding-level with a bit test, and exactly one matches the tag.
func BenchmarkLinearMatch(b *testing.B) {
	subject := subjectOf(translate(b, "urn:epc:tag:sgtin-96:3.0614141.812345.6789"))
	for _, n := range []int{10, 100, 1000, 10000} {
		subs := make([]filter.Subscription, n)
		for i := range subs {
			// Item references differ per subscription; 812345 is the last
			// one, so the scan covers every subscription before matching.
			item := 812345 - (n - 1 - i)
			text := fmt.Sprintf("urn:epc:idpat:sgtin:0614141.%06d.*", item)
			if i%2 == 0 {
				text = fmt.Sprintf("urn:epc:pat:sgtin-96:3.0614141.%06d.*", item)
			}
			subs[i] = filter.Subscription{
				ID:       filter.ID(fmt.Sprint(i)),
				Patterns: []identity.Pattern{compilePattern(b, text)},
			}
		}
		m, err := filter.CompileLinear(subs)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("subscriptions=%d", n), func(b *testing.B) {
			buf := make([]filter.ID, 0, 1)
			b.ReportAllocs()
			for range b.N {
				buf = m.Match(subject, buf[:0])
			}
			if len(buf) != 1 {
				b.Fatalf("matched %d subscriptions, want 1", len(buf))
			}
		})
	}
}
