package filter_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// subjectOf mirrors the application adapter: views into the translation, no
// copies.
func subjectOf(tr tdt.Translation) filter.Subject {
	return filter.Subject{
		Identity: tr.Identity,
		Scheme:   tr.Encoding.Scheme,
		Fields:   tr.Encoding.RawFields(),
		Bits:     tr.Encoding.RawBits(),
		BitLen:   tr.Encoding.BitLen,
	}
}

func translate(t testing.TB, tagURI string) tdt.Translation {
	t.Helper()
	tr, err := tdt.ParseTagURI(tagURI)
	if err != nil {
		t.Fatalf("%s: %v", tagURI, err)
	}
	return tr
}

func compilePattern(t testing.TB, text string) identity.Pattern {
	t.Helper()
	p, err := tdt.CompilePattern(text)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return p
}

// forEachMatcher runs a contract test against every retained matcher.
func forEachMatcher(t *testing.T, test func(t *testing.T, compile filter.Compiler)) {
	for _, m := range []struct {
		name    string
		compile filter.Compiler
	}{
		{"linear", filter.CompileLinear},
		{"index", filter.CompileIndex},
	} {
		t.Run(m.name, func(t *testing.T) { test(t, m.compile) })
	}
}

// TestConformance runs the corpus published by internal/tdt. The matcher must
// agree with Translation.Match, the reference semantics, on every row.
func TestConformance(t *testing.T) {
	forEachMatcher(t, func(t *testing.T, compile filter.Compiler) {
		body, err := os.ReadFile(filepath.Join("..", "tdt", "testdata", "vectors", "patterns.json"))
		if err != nil {
			t.Fatal(err)
		}
		var corpus struct {
			Cases []struct {
				Pattern      string   `json:"pattern"`
				Matches      []string `json:"matches"`
				NonMatches   []string `json:"nonMatches"`
				CompileError string   `json:"compileError"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(body, &corpus); err != nil {
			t.Fatal(err)
		}
		ran := 0
		for _, c := range corpus.Cases {
			if c.CompileError != "" {
				continue // rejected by tdt.CompilePattern before filter sees it
			}
			t.Run(c.Pattern, func(t *testing.T) {
				p := compilePattern(t, c.Pattern)
				m, err := compile([]filter.Subscription{{ID: "s", Patterns: []identity.Pattern{p}}})
				if err != nil {
					t.Fatal(err)
				}
				check := func(tagURI string, want bool) {
					tr := translate(t, tagURI)
					if ref := tr.Match(p); ref != want {
						t.Fatalf("%s: corpus disagrees with Translation.Match", tagURI)
					}
					got := len(m.Match(subjectOf(tr), nil)) == 1
					if got != want {
						t.Errorf("%s: match = %v, want %v", tagURI, got, want)
					}
				}
				for _, uri := range c.Matches {
					check(uri, true)
				}
				for _, uri := range c.NonMatches {
					check(uri, false)
				}
			})
			ran++
		}
		if ran == 0 {
			t.Fatal("conformance corpus is empty")
		}
	})
}

// TestMatchSemantics covers what the single-pattern corpus cannot: alternative
// patterns, result order and deduplication.
func TestMatchSemantics(t *testing.T) {
	forEachMatcher(t, func(t *testing.T, compile filter.Compiler) {
		product := compilePattern(t, "urn:epc:idpat:sgtin:0614141.812345.*")
		filter3 := compilePattern(t, "urn:epc:pat:sgtin-96:3.0614141.812345.*")
		other := compilePattern(t, "urn:epc:idpat:sgtin:0614141.999999.*")
		anySSCC := compilePattern(t, "urn:epc:pat:sscc-96:*.*.*")

		m, err := compile([]filter.Subscription{
			{ID: "both", Patterns: []identity.Pattern{product, filter3}},
			{ID: "other", Patterns: []identity.Pattern{other}},
			{ID: "either", Patterns: []identity.Pattern{other, filter3}},
			{ID: "sscc", Patterns: []identity.Pattern{anySSCC}},
			{ID: "product", Patterns: []identity.Pattern{product}},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			tag  string
			want []filter.ID
		}{
			// Both patterns of "both" match; it is reported once.
			{"urn:epc:tag:sgtin-96:3.0614141.812345.6789", []filter.ID{"both", "either", "product"}},
			// The identity-level pattern ignores the filter value; the encoding
			// level one does not.
			{"urn:epc:tag:sgtin-96:1.0614141.812345.6789", []filter.ID{"both", "product"}},
			{"urn:epc:tag:sgtin-96:1.0614141.999999.6789", []filter.ID{"other", "either"}},
			{"urn:epc:tag:sscc-96:0.0614141.1234567890", []filter.ID{"sscc"}},
			{"urn:epc:tag:giai-96:0.0614141.5678", nil},
		} {
			got := m.Match(subjectOf(translate(t, tc.tag)), nil)
			if !slices.Equal(got, tc.want) {
				t.Errorf("%s: got %v, want %v", tc.tag, got, tc.want)
			}
		}
	})
}

func TestMatchAppendsToCallerBuffer(t *testing.T) {
	forEachMatcher(t, func(t *testing.T, compile filter.Compiler) {
		p := compilePattern(t, "urn:epc:idpat:sgtin:*.*.*")
		m, err := compile([]filter.Subscription{{ID: "all", Patterns: []identity.Pattern{p}}})
		if err != nil {
			t.Fatal(err)
		}
		got := m.Match(subjectOf(translate(t, "urn:epc:tag:sgtin-96:3.0614141.812345.6789")), []filter.ID{"prior"})
		if !slices.Equal(got, []filter.ID{"prior", "all"}) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestCompileRejectsInvalidSubscriptions(t *testing.T) {
	forEachMatcher(t, func(t *testing.T, compile filter.Compiler) {
		p := compilePattern(t, "urn:epc:idpat:sgtin:*.*.*")
		ps := []identity.Pattern{p}
		tooMany := make([]filter.Subscription, filter.MaxSubscriptions+1)
		for i := range tooMany {
			tooMany[i] = filter.Subscription{ID: filter.ID(fmt.Sprint(i)), Patterns: ps}
		}
		for _, tc := range []struct {
			name string
			subs []filter.Subscription
			want error
		}{
			{"empty ID", []filter.Subscription{{Patterns: ps}}, filter.ErrEmptyID},
			{"duplicate ID", []filter.Subscription{{ID: "a", Patterns: ps}, {ID: "a", Patterns: ps}}, filter.ErrDuplicateID},
			{"no patterns", []filter.Subscription{{ID: "a"}}, filter.ErrNoPatterns},
			{"zero pattern", []filter.Subscription{{ID: "a", Patterns: []identity.Pattern{{}}}}, filter.ErrZeroPattern},
			{"too many", tooMany, filter.ErrTooManySubscriptions},
			{"too many patterns", []filter.Subscription{{ID: "a", Patterns: make([]identity.Pattern, filter.MaxPatternsPerSubscription+1)}}, filter.ErrTooManyPatterns},
		} {
			t.Run(tc.name, func(t *testing.T) {
				m, err := compile(tc.subs)
				if !errors.Is(err, tc.want) || m != nil {
					t.Fatalf("got %v, %v; want %v", m, err, tc.want)
				}
			})
		}
		if m, err := compile(nil); err != nil || m.Len() != 0 {
			t.Fatalf("empty subscription list: %v, %v", m, err)
		}
	})
}

func TestCompileCopiesSubscriptions(t *testing.T) {
	forEachMatcher(t, func(t *testing.T, compile filter.Compiler) {
		match := compilePattern(t, "urn:epc:idpat:sgtin:*.*.*")
		miss := compilePattern(t, "urn:epc:idpat:sscc:*.*")
		subs := []filter.Subscription{{ID: "a", Patterns: []identity.Pattern{match}}}
		m, err := compile(subs)
		if err != nil {
			t.Fatal(err)
		}
		subs[0].ID = "changed"
		subs[0].Patterns[0] = miss
		got := m.Match(subjectOf(translate(t, "urn:epc:tag:sgtin-96:3.0614141.812345.6789")), nil)
		if !slices.Equal(got, []filter.ID{"a"}) {
			t.Fatalf("mutating the input changed the matcher: %v", got)
		}
	})
}

func TestSetZeroValueMatchesNothing(t *testing.T) {
	var s filter.Set
	if got := s.Match(subjectOf(translate(t, "urn:epc:tag:sgtin-96:3.0614141.812345.6789")), nil); got != nil {
		t.Fatalf("got %v", got)
	}
	if s.Matcher().Len() != 0 {
		t.Fatal("zero Set holds subscriptions")
	}
}

func TestSetReplaceKeepsCurrentOnError(t *testing.T) {
	p := compilePattern(t, "urn:epc:idpat:sgtin:*.*.*")
	var s filter.Set
	if err := s.Replace([]filter.Subscription{{ID: "a", Patterns: []identity.Pattern{p}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace([]filter.Subscription{{ID: ""}}); !errors.Is(err, filter.ErrEmptyID) {
		t.Fatalf("err = %v", err)
	}
	got := s.Match(subjectOf(translate(t, "urn:epc:tag:sgtin-96:3.0614141.812345.6789")), nil)
	if !slices.Equal(got, []filter.ID{"a"}) {
		t.Fatalf("failed replacement changed the set: %v", got)
	}
}

// TestSetReplaceIsAtomic replaces between two subscription sets while readers
// match. Every result must be exactly one set's result; a partial update would
// show up as a mixture.
func TestSetReplaceIsAtomic(t *testing.T) {
	p := compilePattern(t, "urn:epc:idpat:sgtin:*.*.*")
	build := func(prefix string, n int) []filter.Subscription {
		subs := make([]filter.Subscription, n)
		for i := range subs {
			subs[i] = filter.Subscription{ID: filter.ID(fmt.Sprintf("%s%d", prefix, i)), Patterns: []identity.Pattern{p}}
		}
		return subs
	}
	a, b := build("a", 50), build("b", 70)
	subject := subjectOf(translate(t, "urn:epc:tag:sgtin-96:3.0614141.812345.6789"))

	var s filter.Set
	if err := s.Replace(a); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var readers sync.WaitGroup
	errs := make(chan string, 4)
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			var buf []filter.ID
			for {
				select {
				case <-stop:
					return
				default:
				}
				buf = s.Match(subject, buf[:0])
				prefix := string(buf[0])[:1]
				want := map[string]int{"a": 50, "b": 70}[prefix]
				for _, id := range buf {
					if !strings.HasPrefix(string(id), prefix) {
						errs <- fmt.Sprintf("mixed result: %v", buf)
						return
					}
				}
				if len(buf) != want {
					errs <- fmt.Sprintf("partial result: %d IDs from set %s", len(buf), prefix)
					return
				}
			}
		}()
	}
	for i := range 500 {
		next := a
		if i%2 == 0 {
			next = b
		}
		if err := s.Replace(next); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestMatchDoesNotAllocate(t *testing.T) {
	forEachMatcher(t, func(t *testing.T, compile filter.Compiler) {
		m, err := compile([]filter.Subscription{
			// The index finds "b" before "a" and finds "b" twice, so this
			// covers restoring order and removing duplicates in place.
			{ID: "a", Patterns: []identity.Pattern{compilePattern(t, "urn:epc:idpat:sgtin:0614141.812345.*")}},
			{ID: "b", Patterns: []identity.Pattern{
				compilePattern(t, "urn:epc:pat:sgtin-96:3.0614141.812345.*"),
				compilePattern(t, "urn:epc:pat:sgtin-96:[1-3].0614141.812345.*"),
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		subject := subjectOf(translate(t, "urn:epc:tag:sgtin-96:3.0614141.812345.6789"))
		buf := make([]filter.ID, 0, 2)
		if n := testing.AllocsPerRun(100, func() { buf = m.Match(subject, buf[:0]) }); n != 0 {
			t.Fatalf("Match allocates %v times per call", n)
		}
		if !slices.Equal(buf, []filter.ID{"a", "b"}) {
			t.Fatalf("matched %v, want [a b]", buf)
		}
	})
}

// TestFilterDependsOnlyOnIdentity keeps the #2 boundary: filtering sees
// translations only through Subject and never imports the translator or any
// application package.
func TestFilterDependsOnlyOnIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping dependency check in short mode")
	}
	const module = "github.com/iomz/tagstrak/v2"
	out, err := exec.Command("go", "list", "-deps", module+"/internal/filter").Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, module) && dep != module+"/internal/filter" && dep != module+"/internal/identity" {
			t.Errorf("internal/filter depends on %s", dep)
		}
	}
}
