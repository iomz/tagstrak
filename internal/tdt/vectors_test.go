package tdt_test

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// vector is one golden translation across every level the baseline supports.
type vector struct {
	Scheme       string `json:"scheme"`
	GCPLength    int    `json:"gcpLength"`
	Filter       uint8  `json:"filter"`
	TagURI       string `json:"tagURI"`
	PureIdentity string `json:"pureIdentity"`
	Hex          string `json:"hex"`
	Provenance   string `json:"provenance"`
	Spec         string `json:"spec"`
}

type vectorFile struct {
	Vectors  []vector `json:"vectors"`
	Rejected []struct {
		TagURI string `json:"tagURI"`
		Hex    string `json:"hex"`
		Reason string `json:"reason"`
	} `json:"rejected"`
}

func loadVectors(t *testing.T, name string) vectorFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "vectors", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var f vectorFile
	if err := json.Unmarshal(body, &f); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if len(f.Vectors) == 0 {
		t.Fatalf("%s has no vectors", name)
	}
	return f
}

// TestGoldenVectors runs every golden vector through the whole translation
// surface: decode, promote, render both URI levels, re-encode, and round trip.
func TestGoldenVectors(t *testing.T) {
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	for _, file := range []string{"spec.json", "reference.json"} {
		file := file
		t.Run(file, func(t *testing.T) {
			for _, v := range loadVectors(t, file).Vectors {
				v := v
				t.Run(v.TagURI, func(t *testing.T) {
					bits, err := hex.DecodeString(v.Hex)
					if err != nil {
						t.Fatalf("bad hex: %v", err)
					}

					got, err := translator.Translate(tdt.Input{Bits: bits, BitLen: len(bits) * 8})
					if err != nil {
						t.Fatalf("translate: %v", err)
					}
					if got.Encoding.Scheme != identity.SchemeID(v.Scheme) {
						t.Fatalf("scheme = %s, want %s", got.Encoding.Scheme, v.Scheme)
					}
					if got.Encoding.Control.FilterValue != v.Filter {
						t.Errorf("filter = %d, want %d", got.Encoding.Control.FilterValue, v.Filter)
					}
					if got.Encoding.Control.GCPLength != v.GCPLength {
						t.Errorf("gcp length = %d, want %d", got.Encoding.Control.GCPLength, v.GCPLength)
					}

					pure, err := tdt.PureIdentityURI(got.Identity)
					if err != nil {
						t.Fatalf("pure identity: %v", err)
					}
					if pure != v.PureIdentity {
						t.Errorf("pure identity = %s, want %s", pure, v.PureIdentity)
					}
					tagURI, err := tdt.TagURI(got.Encoding)
					if err != nil {
						t.Fatalf("tag uri: %v", err)
					}
					if tagURI != v.TagURI {
						t.Errorf("tag uri = %s, want %s", tagURI, v.TagURI)
					}

					// Tag URI and binary are bijective.
					reparsed, err := tdt.ParseTagURI(v.TagURI)
					if err != nil {
						t.Fatalf("parse tag uri: %v", err)
					}
					reBits, _ := reparsed.Encoding.Bits()
					if hex.EncodeToString(reBits) != v.Hex {
						t.Errorf("re-encoded %s, want %s", hex.EncodeToString(reBits), v.Hex)
					}
					if !reparsed.Identity.Equal(got.Identity) {
						t.Errorf("identity mismatch after round trip")
					}
					if reparsed.Identity.Key() != got.Identity.Key() {
						t.Errorf("identity key mismatch after round trip")
					}

					// Pure identity is lossy: it round trips only the identity.
					value, err := tdt.ParsePureIdentityURI(v.PureIdentity)
					if err != nil {
						t.Fatalf("parse pure identity: %v", err)
					}
					if !value.Equal(got.Identity) {
						t.Errorf("pure identity round trip changed the identity")
					}

					// Encoding is parameterised, not a reversal: the same
					// identity written with a different filter is still the
					// same identity.
					other := (v.Filter + 1) % 8
					alt, err := tdt.EncodeGS1(value, got.Encoding.Scheme, other)
					if err != nil {
						t.Fatalf("re-encode with filter %d: %v", other, err)
					}
					if !alt.Identity.Equal(got.Identity) {
						t.Errorf("filter value changed the identity")
					}
					altBits, _ := alt.Encoding.Bits()
					if v.Filter != other && hex.EncodeToString(altBits) == v.Hex {
						t.Errorf("filter value did not change the encoding")
					}
				})
			}
		})
	}
}

// TestReferenceDivergences keeps the recorded reference-oracle disagreements
// visible. These inputs are valid; the reference translator simply encoded
// them incorrectly, so tagstrak must encode them correctly instead of
// inheriting the oracle's result.
func TestReferenceDivergences(t *testing.T) {
	file := loadVectors(t, "reference.json")
	if len(file.Rejected) == 0 {
		t.Skip("no recorded divergences")
	}
	for _, r := range file.Rejected {
		got, err := tdt.ParseTagURI(r.TagURI)
		if err != nil {
			t.Errorf("%s: rejected a valid input: %v", r.TagURI, err)
			continue
		}
		bits, bitLen := got.Encoding.Bits()
		if bitLen != got.Encoding.TagLength {
			t.Errorf("%s: encoded %d bits, scheme holds %d", r.TagURI, bitLen, got.Encoding.TagLength)
		}
		if hex.EncodeToString(bits) == r.Hex {
			t.Errorf("%s: reproduced the reference's incorrect encoding (%s)", r.TagURI, r.Reason)
		}
	}
}

// errorClasses maps the names used in the negative corpus to error values.
var errorClasses = map[string]error{
	"ErrUnknownHeader":       tdt.ErrUnknownHeader,
	"ErrUnsupportedScheme":   tdt.ErrUnsupportedScheme,
	"ErrUnrecognisedNonGS1":  tdt.ErrUnrecognisedNonGS1,
	"ErrAmbiguousEncoding":   tdt.ErrAmbiguousEncoding,
	"ErrUnresolvedSemantics": tdt.ErrUnresolvedSemantics,
	"ErrXPCPresent":          tdt.ErrXPCPresent,
	"ErrLengthMismatch":      tdt.ErrLengthMismatch,
	"ErrTruncated":           tdt.ErrTruncated,
	"ErrReservedBits":        tdt.ErrReservedBits,
	"ErrUnknownPartition":    tdt.ErrUnknownPartition,
	"ErrMalformedURI":        tdt.ErrMalformedURI,
	"ErrMalformedPattern":    tdt.ErrMalformedPattern,
	"ErrFieldValue":          tdt.ErrFieldValue,
	"ErrCheckDigit":          tdt.ErrCheckDigit,
	"ErrCannotEncode":        tdt.ErrCannotEncode,
	"ErrPatternStar":         identity.ErrPatternStar,
}

type negativeCase struct {
	Name  string `json:"name"`
	Hex   string `json:"hex"`
	Input string `json:"input"`
	Error string `json:"error"`
	Rule  string `json:"rule"`
}

// TestNegativeVectors checks that every rule produces its typed error rather
// than a silently accepted value.
func TestNegativeVectors(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "vectors", "negative.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Binary  []negativeCase `json:"binary"`
		PureURI []negativeCase `json:"pureURI"`
		TagURI  []negativeCase `json:"tagURI"`
		Pattern []negativeCase `json:"pattern"`
	}
	if err := json.Unmarshal(body, &corpus); err != nil {
		t.Fatal(err)
	}
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))

	run := func(t *testing.T, c negativeCase, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: accepted invalid input", c.Name)
		}
		want, ok := errorClasses[c.Error]
		if !ok {
			t.Fatalf("%s: unknown error class %q", c.Name, c.Error)
		}
		if !errors.Is(err, want) {
			t.Fatalf("%s: error %v is not %s", c.Name, err, c.Error)
		}
		if c.Rule != "" {
			var fe *tdt.FieldError
			if !errors.As(err, &fe) {
				t.Fatalf("%s: error %v is not a field error", c.Name, err)
			}
			if fe.Rule != c.Rule {
				t.Fatalf("%s: rule = %q, want %q", c.Name, fe.Rule, c.Rule)
			}
		}
	}

	for _, c := range corpus.Binary {
		t.Run("binary/"+c.Name, func(t *testing.T) {
			bits, decErr := hex.DecodeString(c.Hex)
			if decErr != nil {
				t.Fatal(decErr)
			}
			_, err := translator.Translate(tdt.Input{Bits: bits, BitLen: len(bits) * 8})
			run(t, c, err)
		})
	}
	for _, c := range corpus.PureURI {
		t.Run("pure/"+c.Name, func(t *testing.T) {
			_, err := tdt.ParsePureIdentityURI(c.Input)
			run(t, c, err)
		})
	}
	for _, c := range corpus.TagURI {
		t.Run("tag/"+c.Name, func(t *testing.T) {
			_, err := tdt.ParseTagURI(c.Input)
			run(t, c, err)
		})
	}
	for _, c := range corpus.Pattern {
		t.Run("pattern/"+c.Name, func(t *testing.T) {
			_, err := tdt.CompilePattern(c.Input)
			run(t, c, err)
		})
	}
}
