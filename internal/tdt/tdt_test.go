package tdt_test

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
	"github.com/iomz/tagstrak/v2/internal/tdt/iso"
)

const sgtinHex = "3074257bf7194e4000001a85"

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func transportUnitIdentity(t *testing.T) identity.Value {
	t.Helper()
	// "1J" plus fourteen characters is sixteen characters, six whole words.
	v, err := identity.New(identity.AuthorityISO15459, identity.KeyTypeTransportUnit, []identity.Component{
		{Name: identity.DataIdentifier, Value: "1J"},
		{Name: identity.ISOIdentifier, Value: "UN043325711MH8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestRecognitionUsesProtocolControl checks that GS1 and non-GS1 data are told
// apart by the Gen2 PC numbering-system toggle rather than by the first byte.
func TestRecognitionUsesProtocolControl(t *testing.T) {
	translator := tdt.New()
	gs1Bits := mustHex(t, sgtinHex)

	got, err := translator.Translate(tdt.Input{Bits: gs1Bits, BitLen: 96, PC: 0x3000, HasPC: true})
	if err != nil {
		t.Fatalf("gs1 translate: %v", err)
	}
	if got.Encoding.Scheme != "sgtin-96" {
		t.Errorf("scheme = %s, want sgtin-96", got.Encoding.Scheme)
	}
	if got.Encoding.Control.Toggle {
		t.Errorf("GS1 data must not set the toggle")
	}

	// The same bits with the toggle set are not an EPC, whatever the leading
	// byte looks like. Without a registered AFI they are not claimed at all.
	if _, err := translator.Translate(tdt.Input{Bits: gs1Bits, BitLen: 96, PC: 0x3142, HasPC: true}); !errors.Is(err, tdt.ErrUnrecognisedNonGS1) {
		t.Errorf("non-GS1 with unregistered AFI: error = %v, want ErrUnrecognisedNonGS1", err)
	}
}

// TestISOThroughProtocolControl translates an ISO tag end to end.
func TestISOThroughProtocolControl(t *testing.T) {
	enc, err := tdt.EncodeISO(transportUnitIdentity(t), iso.SchemeTransportUnit)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	bits, bitLen := enc.Encoding.Bits()
	pc := tdt.ProtocolControl(enc.Encoding)
	if pc&0x0100 == 0 {
		t.Errorf("PC 0x%04X does not set the numbering-system toggle", pc)
	}
	if uint8(pc&0xFF) != iso.AFITransportUnit {
		t.Errorf("PC 0x%04X carries AFI 0x%02X, want 0x%02X", pc, uint8(pc&0xFF), iso.AFITransportUnit)
	}

	translator := tdt.New()
	// BitLen zero exercises deriving the length from the PC word.
	got, err := translator.Translate(tdt.Input{Bits: bits, PC: pc, HasPC: true})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if got.Encoding.Scheme != iso.SchemeTransportUnit {
		t.Errorf("scheme = %s, want %s", got.Encoding.Scheme, iso.SchemeTransportUnit)
	}
	if !got.Identity.Equal(enc.Identity) {
		t.Errorf("identity changed: %s", got.Identity.Describe())
	}
	if got.Encoding.BitLen != bitLen {
		t.Errorf("bit length = %d, want %d", got.Encoding.BitLen, bitLen)
	}
}

// TestAmbiguousWithoutProtocolControl checks that a missing PC word is an
// error rather than a guess, unless a default is configured explicitly.
func TestAmbiguousWithoutProtocolControl(t *testing.T) {
	bits := mustHex(t, sgtinHex)
	if _, err := tdt.New().Translate(tdt.Input{Bits: bits, BitLen: 96}); !errors.Is(err, tdt.ErrAmbiguousEncoding) {
		t.Errorf("error = %v, want ErrAmbiguousEncoding", err)
	}
	got, err := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1)).Translate(tdt.Input{Bits: bits, BitLen: 96})
	if err != nil {
		t.Fatalf("configured default: %v", err)
	}
	if got.Encoding.Scheme != "sgtin-96" {
		t.Errorf("scheme = %s", got.Encoding.Scheme)
	}
}

// TestIdentityCarriesNoEncodingControl is the central domain invariant.
func TestIdentityCarriesNoEncodingControl(t *testing.T) {
	got, err := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1)).
		Translate(tdt.Input{Bits: mustHex(t, sgtinHex), BitLen: 96})
	if err != nil {
		t.Fatal(err)
	}
	for c := range got.Identity.Components() {
		switch c.Name {
		case identity.FilterValue, identity.Partition:
			t.Errorf("identity carries encoding-control component %s", c.Name)
		}
	}
	if got.Identity.Len() != 3 {
		t.Errorf("identity has %d components, want 3", got.Identity.Len())
	}
	if n := len(got.EncodingFields()); n != 4 {
		t.Errorf("encoding vector has %d components, want 4", n)
	}
	if got.EncodingFields()[0].Name != identity.FilterValue {
		t.Errorf("encoding vector does not lead with the filter value")
	}
}

// TestNoImplicitPromotion checks that an identity too large for the requested
// encoding is an error, never a silent switch to a longer scheme.
func TestNoImplicitPromotion(t *testing.T) {
	// GIAI's pure identity allows a thirteen digit asset reference, but the
	// 96-bit encoding holds 42 bits with a twelve digit company prefix.
	value, err := tdt.ParsePureIdentityURI("urn:epc:id:giai:000000000000.9999999999999")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tdt.EncodeGS1(value, "giai-96", 0); !errors.Is(err, tdt.ErrFieldValue) {
		t.Errorf("error = %v, want a field error", err)
	}
	if _, err := tdt.EncodeGS1(value, "giai-202", 0); !errors.Is(err, tdt.ErrUnsupportedScheme) {
		t.Errorf("error = %v, want ErrUnsupportedScheme", err)
	}
}

// TestSupportedSurface pins the v2.0.0 support commitment and its provenance.
func TestSupportedSurface(t *testing.T) {
	want := map[identity.SchemeID]bool{
		"sgtin-96": true, "sscc-96": true, "giai-96": true, "grai-96": true,
		iso.SchemeTransportUnit: true, iso.Scheme17363: true,
	}
	got := tdt.SupportedSchemes()
	if len(got) != len(want) {
		t.Fatalf("supported schemes = %v", got)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected supported scheme %s", s)
		}
	}
	src := tdt.SourceInfo()
	if src.TDSVersion != "2.2" {
		t.Errorf("TDS version = %s, want 2.2", src.TDSVersion)
	}
	if src.Commit == "" || len(src.Files) == 0 {
		t.Errorf("provenance is incomplete: %+v", src)
	}
}

// TestProtocolControlLengthIsAuthoritative checks that the PC length field
// bounds the UII and that a disagreeing observed length is reported rather
// than silently accepted in either direction.
func TestProtocolControlLengthIsAuthoritative(t *testing.T) {
	translator := tdt.New()
	bits := mustHex(t, sgtinHex)

	// The PC declares six words; the caller agrees.
	if _, err := translator.Translate(tdt.Input{Bits: bits, BitLen: 96, PC: 0x3000, HasPC: true}); err != nil {
		t.Fatalf("agreeing lengths: %v", err)
	}
	// The PC declares six words and the caller says otherwise.
	for _, bitLen := range []int{80, 112} {
		_, err := translator.Translate(tdt.Input{Bits: append(bits, 0, 0), BitLen: bitLen, PC: 0x3000, HasPC: true})
		if !errors.Is(err, tdt.ErrLengthMismatch) {
			t.Errorf("bitLen %d: error = %v, want ErrLengthMismatch", bitLen, err)
		}
	}
	// With no observed length the PC supplies it.
	got, err := translator.Translate(tdt.Input{Bits: bits, PC: 0x3000, HasPC: true})
	if err != nil {
		t.Fatalf("derived length: %v", err)
	}
	if got.Encoding.BitLen != 96 {
		t.Errorf("bit length = %d, want 96", got.Encoding.BitLen)
	}
	// A zero-length UII is not a tag.
	if _, err := translator.Translate(tdt.Input{Bits: bits, PC: 0x0000, HasPC: true}); !errors.Is(err, tdt.ErrTruncated) {
		t.Errorf("zero length: error = %v, want ErrTruncated", err)
	}
}

// TestPacketPCWithXPCIsRefused checks decision B4: when XI is asserted the
// length field covers one or two XPC words that precede the UII, so L*16 is
// not the UII extent and the surplus is never consumed as identifier data.
func TestPacketPCWithXPCIsRefused(t *testing.T) {
	translator := tdt.New()
	bits := mustHex(t, sgtinHex)

	// StoredPC, XI clear: L is the UII extent, as before.
	if _, err := translator.Translate(tdt.Input{Bits: bits, BitLen: 96, PC: 0x3000, HasPC: true}); err != nil {
		t.Fatalf("XI clear: %v", err)
	}

	// PacketPC with XI asserted. The tag sends XPC_W1 before the UII and has
	// incremented L to 7 to cover it, so 96 of the 112 declared bits are UII
	// and 16 are XPC. Refused in both length directions.
	xi := uint16(0x3800 | 0x0200) // L=7, XI=1, T=0
	if _, err := translator.Translate(tdt.Input{Bits: append(bits, 0, 0), BitLen: 112, PC: xi, HasPC: true}); !errors.Is(err, tdt.ErrXPCPresent) {
		t.Errorf("caller-supplied length: error = %v, want ErrXPCPresent", err)
	}
	if _, err := translator.Translate(tdt.Input{Bits: append(bits, 0, 0), PC: xi, HasPC: true}); !errors.Is(err, tdt.ErrXPCPresent) {
		t.Errorf("derived length: error = %v, want ErrXPCPresent", err)
	}

	// Double increment, XEB also set on the tag: L=8, two XPC words.
	xi2 := uint16(0x4000 | 0x0200)
	if _, err := translator.Translate(tdt.Input{Bits: append(bits, 0, 0, 0, 0), PC: xi2, HasPC: true}); !errors.Is(err, tdt.ErrXPCPresent) {
		t.Errorf("double increment: error = %v, want ErrXPCPresent", err)
	}

	// It applies to ISO data too, which is where it bites hardest: a tag that
	// signals hazardous material sets XPC_W1 bit 21Fh, which forces XI.
	isoXI := uint16(0x3800 | 0x0200 | 0x0100 | uint16(iso.AFITransportUnit))
	if _, err := translator.Translate(tdt.Input{Bits: bits, PC: isoXI, HasPC: true}); !errors.Is(err, tdt.ErrXPCPresent) {
		t.Errorf("ISO with XI: error = %v, want ErrXPCPresent", err)
	}
}

// TestProtocolControlEmitsAStoredPC checks that what we write back is a
// StoredPC: XI and RUM are computed by the tag and are left clear.
func TestProtocolControlEmitsAStoredPC(t *testing.T) {
	enc, err := tdt.EncodeISO(transportUnitIdentity(t), iso.SchemeTransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	pc := tdt.ProtocolControl(enc.Encoding)
	if pc&0x0200 != 0 {
		t.Errorf("PC 0x%04X asserts XI; ProtocolControl emits a StoredPC", pc)
	}
	if pc&0x0400 != 0 {
		t.Errorf("PC 0x%04X asserts RUM; that bit is computed by the tag", pc)
	}
	if pc&0x0100 == 0 || uint8(pc&0x00FF) != iso.AFITransportUnit {
		t.Errorf("PC 0x%04X must carry T=1 and AFI 0xA2", pc)
	}
	// A StoredPC that round trips through Translate must still decode.
	bits, bitLen := enc.Encoding.Bits()
	if _, err := tdt.New().Translate(tdt.Input{Bits: bits, BitLen: bitLen, PC: pc, HasPC: true}); err != nil {
		t.Fatalf("emitted StoredPC did not decode: %v", err)
	}
}

// TestISO17363IsNotPromoted checks decision A7 at the translation boundary:
// the tag is recognised and its characters decode, but it does not become a
// structured identity.
func TestISO17363IsNotPromoted(t *testing.T) {
	// Eight characters, three words, so the character stage is unambiguous.
	bits, bitLen, err := iso.EncodeText("probe", "7BCSQU30")
	if err != nil {
		t.Fatal(err)
	}
	pc := uint16(bitLen/16)<<11 | 0x0100 | uint16(iso.AFI17363)
	_, err = tdt.New().Translate(tdt.Input{Bits: bits, BitLen: bitLen, PC: pc, HasPC: true})
	if !errors.Is(err, tdt.ErrUnresolvedSemantics) {
		t.Errorf("error = %v, want ErrUnresolvedSemantics", err)
	}
	text, err := iso.DecodeText(bits, bitLen)
	if err != nil {
		t.Fatalf("character decoding must still work: %v", err)
	}
	if text.Characters != "7BCSQU30" {
		t.Errorf("characters = %q", text.Characters)
	}
}

// TestUnsupportedRegisteredAFI checks that a registered but out-of-baseline
// construct is reported as unsupported, not as unrecognised.
func TestUnsupportedRegisteredAFI(t *testing.T) {
	bits, bitLen, err := iso.EncodeText("probe", "25SABCDE")
	if err != nil {
		t.Fatal(err)
	}
	pc := uint16(bitLen/16)<<11 | 0x0100 | 0xA1
	_, err = tdt.New().Translate(tdt.Input{Bits: bits, BitLen: bitLen, PC: pc, HasPC: true})
	if !errors.Is(err, tdt.ErrUnsupportedScheme) {
		t.Errorf("error = %v, want ErrUnsupportedScheme", err)
	}
}
