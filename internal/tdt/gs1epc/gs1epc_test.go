package gs1epc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

// TestLayoutMatchesArtefactPatterns cross-checks the generated bit layout
// against the regular expressions the upstream artefacts publish.
//
// The regular expressions are test data only: the decode path slices bits
// directly, because translation is on the hot path and TDS regexes are not.
func TestLayoutMatchesArtefactPatterns(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "tdtdef", "testdata", "binary_patterns.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Scheme    string `json:"scheme"`
		GCPLength int    `json:"gcpLength"`
		Pattern   string `json:"pattern"`
	}
	if err := json.Unmarshal(body, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) == 0 {
		t.Fatal("no artefact patterns")
	}

	for _, v := range vectors {
		v := v
		t.Run(fmt.Sprintf("%s/%d", v.Scheme, v.GCPLength), func(t *testing.T) {
			re := regexp.MustCompile(v.Pattern)
			s, err := Lookup(identity.SchemeID(strings.ToLower(v.Scheme)))
			if err != nil {
				t.Fatal(err)
			}
			opt, ok := s.def.Option(v.GCPLength)
			if !ok {
				t.Fatalf("no option for gcp length %d", v.GCPLength)
			}
			// Encode a value that exercises every field, then check the bit
			// string against the artefact's own grammar.
			comps := make([]identity.Component, len(opt.PureFields))
			for i, f := range opt.PureFields {
				comps[i] = identity.Component{Name: componentNames[f.Name], Value: render(f, f.DecimalMax)}
			}
			value, err := identity.New(identity.AuthorityGS1, s.keyType, comps)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := Encode(value, EncodeParams{Scheme: s.id, FilterValue: 5})
			if err != nil {
				// The pure identity maximum can exceed what the binary field
				// holds; that is a rejection, not a layout failure.
				if errors.Is(err, tdtcore.ErrFieldValue) {
					t.Skipf("pure identity maximum does not fit the encoding: %v", err)
				}
				t.Fatal(err)
			}
			bits, bitLen := tr.Encoding.Bits()
			if !re.MatchString(bitString(bits, bitLen)) {
				t.Errorf("encoding %s does not match artefact pattern %s", bitString(bits, bitLen), v.Pattern)
			}
		})
	}
}

func bitString(data []byte, bitLen int) string {
	var b strings.Builder
	for i := 0; i < bitLen; i++ {
		if data[i/8]>>(7-uint(i%8))&1 == 1 {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// TestEveryOptionRoundTrips walks every scheme and company prefix length.
func TestEveryOptionRoundTrips(t *testing.T) {
	for _, s := range Schemes() {
		for _, opt := range s.def.Options {
			comps := make([]identity.Component, len(opt.PureFields))
			for i, f := range opt.PureFields {
				// Use the binary maximum so the value is always encodable.
				max := opt.BinaryFields[i+1].DecimalMax
				if f.DecimalMax < max {
					max = f.DecimalMax
				}
				comps[i] = identity.Component{Name: componentNames[f.Name], Value: render(f, max)}
			}
			value, err := identity.New(identity.AuthorityGS1, s.keyType, comps)
			if err != nil {
				t.Fatal(err)
			}
			for filter := uint8(0); filter < 8; filter++ {
				tr, err := Encode(value, EncodeParams{Scheme: s.id, FilterValue: filter})
				if err != nil {
					t.Fatalf("%s/%d filter %d: %v", s.id, opt.GCPLength, filter, err)
				}
				bits, bitLen := tr.Encoding.Bits()
				back, err := Decode(bits, bitLen)
				if err != nil {
					t.Fatalf("%s/%d: decode: %v", s.id, opt.GCPLength, err)
				}
				if !back.Identity.Equal(value) {
					t.Errorf("%s/%d: identity changed", s.id, opt.GCPLength)
				}
				if back.Encoding.Control.FilterValue != filter {
					t.Errorf("%s/%d: filter = %d, want %d", s.id, opt.GCPLength, back.Encoding.Control.FilterValue, filter)
				}
				if back.Encoding.Control.GCPLength != opt.GCPLength {
					t.Errorf("%s: gcp length = %d, want %d", s.id, back.Encoding.Control.GCPLength, opt.GCPLength)
				}
				uri, err := TagURI(back.Encoding)
				if err != nil {
					t.Fatal(err)
				}
				again, err := ParseTagURI(uri)
				if err != nil {
					t.Fatalf("%s: %v", uri, err)
				}
				reBits, _ := again.Encoding.Bits()
				if string(reBits) != string(bits) {
					t.Errorf("%s: tag uri round trip changed the bits", uri)
				}
			}
		}
	}
}

// TestSchemeRegistryIsComplete guards the support allowlist.
func TestSchemeRegistryIsComplete(t *testing.T) {
	if len(Schemes()) != len(supported) {
		t.Fatalf("registry has %d schemes, allowlist has %d", len(Schemes()), len(supported))
	}
	if _, err := Lookup("sgtin-198"); !errors.Is(err, tdtcore.ErrUnsupportedScheme) {
		t.Errorf("unsupported scheme error = %v", err)
	}
	for _, s := range Schemes() {
		if s.KeyType() == identity.KeyTypeUnknown {
			t.Errorf("%s has no key type", s.ID())
		}
		if s.TagLength() != 96 {
			t.Errorf("%s tag length = %d", s.ID(), s.TagLength())
		}
	}
}
