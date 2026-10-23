package tdt_test

import (
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
	"github.com/iomz/tagstrak/v2/internal/tdt/iso"
)

// Benchmarks establish the v2 baseline for representative translation work:
// decoding a tag, rendering identifiers, compiling a subscription pattern, and
// the two matching paths internal/filter can take.

func BenchmarkTranslateSGTIN96(b *testing.B) {
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	bits := mustHexBytes("3074257bf7194e4000001a85")
	in := tdt.Input{Bits: bits, BitLen: 96, PC: 0x3000, HasPC: true}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := translator.Translate(in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTranslateISOTransportUnit(b *testing.B) {
	value, err := identity.New(identity.AuthorityISO15459, identity.KeyTypeTransportUnit, []identity.Component{
		{Name: identity.DataIdentifier, Value: "1J"},
		{Name: identity.ISOIdentifier, Value: "UN043325711MH8"},
	})
	if err != nil {
		b.Fatal(err)
	}
	enc, err := tdt.EncodeISO(value, iso.SchemeTransportUnit)
	if err != nil {
		b.Fatal(err)
	}
	bits, bitLen := enc.Encoding.Bits()
	in := tdt.Input{Bits: bits, BitLen: bitLen, PC: tdt.ProtocolControl(enc.Encoding), HasPC: true}
	translator := tdt.New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := translator.Translate(in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPureIdentityURI(b *testing.B) {
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	got, err := translator.Translate(tdt.Input{Bits: mustHexBytes("3074257bf7194e4000001a85"), BitLen: 96})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := tdt.PureIdentityURI(got.Identity); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeSGTIN96(b *testing.B) {
	value, err := tdt.ParsePureIdentityURI("urn:epc:id:sgtin:0614141.812345.6789")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := tdt.EncodeGS1(value, "sgtin-96", 3); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompilePattern(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := tdt.CompilePattern("urn:epc:pat:sgtin-96:3.0614141.812345.*"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMatchIdentityFields(b *testing.B) {
	p, err := tdt.CompilePattern("urn:epc:idpat:sgtin:0614141.812345.*")
	if err != nil {
		b.Fatal(err)
	}
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	got, err := translator.Translate(tdt.Input{Bits: mustHexBytes("3074257bf7194e4000001a85"), BitLen: 96})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !got.Match(p) {
			b.Fatal("expected a match")
		}
	}
}

func BenchmarkMatchEncodingBits(b *testing.B) {
	p, err := tdt.CompilePattern("urn:epc:pat:sgtin-96:3.0614141.812345.*")
	if err != nil {
		b.Fatal(err)
	}
	bits := p.Bits()
	if bits == nil {
		b.Fatal("expected a bit test")
	}
	tag := mustHexBytes("3074257bf7194e4000001a85")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !bits.Match(tag, 96) {
			b.Fatal("expected a match")
		}
	}
}
