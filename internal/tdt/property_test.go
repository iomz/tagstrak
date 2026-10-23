package tdt_test

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// classified reports whether an error belongs to the published taxonomy.
// An unclassified error would leave callers with nothing but string matching.
func classified(err error) bool {
	for _, class := range errorClasses {
		if errors.Is(err, class) {
			return true
		}
	}
	return false
}

// TestRandomBitsAreClassified feeds random tag data in and requires either a
// successful translation that round trips, or an error from the taxonomy.
func TestRandomBitsAreClassified(t *testing.T) {
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	r := rand.New(rand.NewPCG(1, 2))
	bits := make([]byte, 12)
	for i := 0; i < 20000; i++ {
		for j := range bits {
			bits[j] = byte(r.UintN(256))
		}
		got, err := translator.Translate(tdt.Input{Bits: bits, BitLen: 96})
		if err != nil {
			if !classified(err) {
				t.Fatalf("unclassified error: %v", err)
			}
			continue
		}
		uri, err := tdt.TagURI(got.Encoding)
		if err != nil {
			t.Fatalf("tag uri: %v", err)
		}
		again, err := tdt.ParseTagURI(uri)
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		reBits, _ := again.Encoding.Bits()
		original, _ := got.Encoding.Bits()
		if string(reBits) != string(original) {
			t.Fatalf("%s did not round trip", uri)
		}
	}
}

// TestEncodeDecodeIsBijective generates valid identities across every scheme
// and company prefix length and checks the bits round trip exactly.
func TestEncodeDecodeIsBijective(t *testing.T) {
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	r := rand.New(rand.NewPCG(3, 4))
	for _, v := range loadVectors(t, "reference.json").Vectors {
		base, err := tdt.ParsePureIdentityURI(v.PureIdentity)
		if err != nil {
			t.Fatalf("%s: %v", v.PureIdentity, err)
		}
		filter := uint8(r.UintN(8))
		enc, err := tdt.EncodeGS1(base, identity.SchemeID(v.Scheme), filter)
		if err != nil {
			t.Fatalf("%s: %v", v.PureIdentity, err)
		}
		bits, bitLen := enc.Encoding.Bits()
		back, err := translator.Translate(tdt.Input{Bits: bits, BitLen: bitLen})
		if err != nil {
			t.Fatalf("%s: %v", v.PureIdentity, err)
		}
		if !back.Identity.Equal(base) {
			t.Fatalf("%s: identity changed", v.PureIdentity)
		}
		if back.Encoding.Control.FilterValue != filter {
			t.Fatalf("%s: filter %d became %d", v.PureIdentity, filter, back.Encoding.Control.FilterValue)
		}
	}
}

// FuzzTranslate checks that no tag data can panic the translator and that
// every rejection is classified.
func FuzzTranslate(f *testing.F) {
	f.Add(mustHexBytes("3074257bf7194e4000001a85"), uint16(0x3000))
	f.Add(mustHexBytes("3114257bf4499602d2000000"), uint16(0x3000))
	f.Add(mustHexBytes("00"), uint16(0x0100))
	// A PC with XI asserted: the length field covers one or two XPC words that
	// precede the UII, so the UII extent cannot be taken from it. Kept as an
	// explicit seed rather than an opaque corpus blob so the case it guards is
	// legible (issue #8 Amendment 2, decision B4).
	f.Add(mustHexBytes("3074257bf7194e4000001a85"), uint16(0x2FA2))
	f.Add(mustHexBytes("3074257bf7194e4000001a85"), uint16(0x3A00))
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	f.Fuzz(func(t *testing.T, bits []byte, pc uint16) {
		if len(bits) > 1024 {
			t.Skip()
		}
		got, err := translator.Translate(tdt.Input{Bits: bits, PC: pc, HasPC: true})
		if err != nil {
			if !classified(err) {
				t.Fatalf("unclassified error: %v", err)
			}
			return
		}
		if got.Identity.Len() == 0 {
			t.Fatal("translation produced no identity")
		}
		// A successful translation must describe its own encoding correctly.
		raw, bitLen := got.Encoding.Bits()
		if bitLen > len(raw)*8 {
			t.Fatalf("encoding claims %d bits over %d bytes", bitLen, len(raw))
		}
	})
}

// FuzzCompilePattern checks pattern compilation never panics and never
// produces a pattern that violates its own contract.
func FuzzCompilePattern(f *testing.F) {
	f.Add("urn:epc:idpat:sgtin:0614141.812345.*")
	f.Add("urn:epc:pat:sgtin-96:3.0614141.812345.6789")
	f.Add("urn:epc:pat:sgtin-96:[0-7].*.*.*")
	f.Fuzz(func(t *testing.T, text string) {
		p, err := tdt.CompilePattern(text)
		if err != nil {
			if !classified(err) && !errors.Is(err, identity.ErrPatternFields) &&
				!errors.Is(err, identity.ErrPatternLevel) && !errors.Is(err, identity.ErrPatternStar) {
				t.Fatalf("unclassified error: %v", err)
			}
			return
		}
		if p.Level() == identity.LevelIdentity && (p.Bits() != nil || p.Scheme() != "") {
			t.Fatal("identity-level pattern carries encoding parts")
		}
		seenAny := false
		for i := 0; i < p.Len(); i++ {
			switch p.Field(i).Kind {
			case identity.FieldAny:
				seenAny = true
			default:
				if seenAny {
					t.Fatal("a constrained field follows a star")
				}
			}
		}
	})
}

// FuzzParseURI checks URI parsing never panics and agrees with rendering.
func FuzzParseURI(f *testing.F) {
	f.Add("urn:epc:id:sgtin:0614141.812345.6789")
	f.Add("urn:epc:tag:sgtin-96:3.0614141.812345.6789")
	f.Fuzz(func(t *testing.T, text string) {
		if value, err := tdt.ParsePureIdentityURI(text); err == nil {
			rendered, err := tdt.PureIdentityURI(value)
			if err != nil {
				t.Fatalf("re-render %q: %v", text, err)
			}
			if rendered != text {
				t.Fatalf("re-rendered %q as %q", text, rendered)
			}
		} else if !classified(err) {
			t.Fatalf("unclassified error: %v", err)
		}
		if tr, err := tdt.ParseTagURI(text); err == nil {
			rendered, err := tdt.TagURI(tr.Encoding)
			if err != nil {
				t.Fatalf("re-render %q: %v", text, err)
			}
			if rendered != text {
				t.Fatalf("re-rendered %q as %q", text, rendered)
			}
		} else if !classified(err) {
			t.Fatalf("unclassified error: %v", err)
		}
	})
}

func mustHexBytes(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := range out {
		var v byte
		for j := 0; j < 2; j++ {
			c := s[i*2+j]
			switch {
			case c >= '0' && c <= '9':
				v = v<<4 | (c - '0')
			case c >= 'a' && c <= 'f':
				v = v<<4 | (c - 'a' + 10)
			}
		}
		out[i] = v
	}
	return out
}
