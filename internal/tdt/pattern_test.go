package tdt_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

type patternCase struct {
	Pattern      string   `json:"pattern"`
	Level        string   `json:"level"`
	Matches      []string `json:"matches"`
	NonMatches   []string `json:"nonMatches"`
	CompileError string   `json:"compileError"`
}

func loadPatternCases(t *testing.T) []patternCase {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "vectors", "patterns.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []patternCase `json:"cases"`
	}
	if err := json.Unmarshal(body, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus.Cases
}

// TestPatternConformance is the published match-semantics corpus. Optimised
// matchers in internal/filter must produce these results too.
func TestPatternConformance(t *testing.T) {
	for _, c := range loadPatternCases(t) {
		c := c
		t.Run(c.Pattern, func(t *testing.T) {
			p, err := tdt.CompilePattern(c.Pattern)
			if c.CompileError != "" {
				if err == nil {
					t.Fatalf("compiled an invalid pattern")
				}
				if !errors.Is(err, errorClasses[c.CompileError]) {
					t.Fatalf("error %v is not %s", err, c.CompileError)
				}
				return
			}
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			wantLevel := map[string]identity.PatternLevel{
				"identity": identity.LevelIdentity,
				"encoding": identity.LevelEncoding,
			}[c.Level]
			if p.Level() != wantLevel {
				t.Fatalf("level = %s, want %s", p.Level(), c.Level)
			}
			if p.Level() == identity.LevelIdentity && p.Bits() != nil {
				t.Fatalf("identity-level pattern carries a bit test")
			}

			check := func(tagURI string, want bool) {
				t.Helper()
				tr, err := tdt.ParseTagURI(tagURI)
				if err != nil {
					t.Fatalf("%s: %v", tagURI, err)
				}
				if got := tr.Match(p); got != want {
					t.Errorf("%s: match = %v, want %v", tagURI, got, want)
				}
				// Where a bit test is published it must agree with field
				// matching, because that is the condition for publishing it.
				if bits := p.Bits(); bits != nil && p.Scheme() == tr.Encoding.Scheme {
					raw, bitLen := tr.Encoding.Bits()
					if got := bits.Match(raw, bitLen); got != want {
						t.Errorf("%s: bit match = %v, want %v", tagURI, got, want)
					}
				}
			}
			for _, uri := range c.Matches {
				check(uri, true)
			}
			for _, uri := range c.NonMatches {
				check(uri, false)
			}
		})
	}
}

// TestIdentityPatternsIgnoreEncoding is the property that motivates the whole
// domain split: an identity-level pattern must be blind to the filter value.
func TestIdentityPatternsIgnoreEncoding(t *testing.T) {
	p, err := tdt.CompilePattern("urn:epc:idpat:sgtin:0614141.812345.6789")
	if err != nil {
		t.Fatal(err)
	}
	value, err := tdt.ParsePureIdentityURI("urn:epc:id:sgtin:0614141.812345.6789")
	if err != nil {
		t.Fatal(err)
	}
	for filter := uint8(0); filter < 8; filter++ {
		tr, err := tdt.EncodeGS1(value, "sgtin-96", filter)
		if err != nil {
			t.Fatalf("filter %d: %v", filter, err)
		}
		if !tr.Match(p) {
			t.Errorf("filter %d: identity pattern did not match", filter)
		}
	}
}

// TestEncodingPatternsSeeEncoding is the converse: the encoding level does
// constrain the filter value.
func TestEncodingPatternsSeeEncoding(t *testing.T) {
	p, err := tdt.CompilePattern("urn:epc:pat:sgtin-96:3.0614141.812345.*")
	if err != nil {
		t.Fatal(err)
	}
	value, err := tdt.ParsePureIdentityURI("urn:epc:id:sgtin:0614141.812345.6789")
	if err != nil {
		t.Fatal(err)
	}
	for filter := uint8(0); filter < 8; filter++ {
		tr, err := tdt.EncodeGS1(value, "sgtin-96", filter)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := tr.Match(p), filter == 3; got != want {
			t.Errorf("filter %d: match = %v, want %v", filter, got, want)
		}
	}
}

// TestBitPatternKeepsTrailingLiterals checks that a trailing star does not
// unpin literals that follow it: SSCC-96's reserved bits sit after the serial
// reference and must stay zero.
func TestBitPatternKeepsTrailingLiterals(t *testing.T) {
	p, err := tdt.CompilePattern("urn:epc:pat:sscc-96:0.0614141.*")
	if err != nil {
		t.Fatal(err)
	}
	bits := p.Bits()
	if bits == nil {
		t.Fatal("expected a bit test for an exact company prefix")
	}
	value, err := tdt.ParsePureIdentityURI("urn:epc:id:sscc:0614141.1234567890")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := tdt.EncodeGS1(value, "sscc-96", 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, bitLen := tr.Encoding.Bits()
	if !bits.Match(raw, bitLen) {
		t.Fatal("bit test rejected a valid tag")
	}
	reserved := append([]byte(nil), raw...)
	reserved[len(reserved)-1] |= 1 // bit 95, inside the reserved literal
	if bits.Match(reserved, bitLen) {
		t.Fatal("bit test accepted a tag with nonzero reserved bits")
	}
}

// TestPatternsAreImmutable checks that a compiled pattern hands out no
// references a consumer could mutate.
func TestPatternsAreImmutable(t *testing.T) {
	p, err := tdt.CompilePattern("urn:epc:pat:sgtin-96:3.0614141.812345.*")
	if err != nil {
		t.Fatal(err)
	}
	fields := p.Fields()
	fields[0].Text = "tampered"
	if p.Field(0).Text != "3" {
		t.Fatal("mutating the returned fields changed the pattern")
	}
	bits := p.Bits()
	if bits == nil {
		t.Fatal("expected a bit test for a fully determined pattern")
	}
	mask := bits.Mask()
	for i := range mask {
		mask[i] = 0
	}
	if got := bits.Mask(); got[0] == 0 {
		t.Fatal("mutating the returned mask changed the pattern")
	}
}
