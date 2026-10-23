package iso

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

// annexARow is one row of the Annex A transcription.
type annexARow struct {
	Code          string `json:"code"`
	Value         int    `json:"value"`
	Character     string `json:"character"`
	Control       string `json:"control"`
	Byte          string `json:"byte"`
	DataCharacter bool   `json:"dataCharacter"`
}

// characterSet loads the Annex A transcription.
func characterSet(t *testing.T) []annexARow {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "testdata", "vectors", "iso.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		CharacterSet struct {
			Rows []annexARow `json:"rows"`
		} `json:"characterSet"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.CharacterSet.Rows) != 64 {
		t.Fatalf("character set has %d rows, want 64", len(doc.CharacterSet.Rows))
	}
	return doc.CharacterSet.Rows
}

// TestAnnexAMapping checks the codec against the transcribed register table.
//
// This is the only ISO evidence that is external to tagstrak: the table
// itself. It is not an encoded-tag example, so it demonstrates the mapping,
// not interoperability.
func TestAnnexAMapping(t *testing.T) {
	var dataRows, controlRows int
	for _, row := range characterSet(t) {
		if !row.DataCharacter {
			controlRows++
			if _, ok := fromCode(byte(row.Value)); ok {
				t.Errorf("%s holds control %s but decoded as a data character", row.Code, row.Control)
			}
			// The character the plain ASCII & 0x3F mask would give for this
			// code must not be encodable, because this code is not it.
			displaced := byte(row.Value)
			if displaced < 0x20 {
				displaced += 0x40
			}
			if accepted(displaced) {
				t.Errorf("%q occupies control position %s and must not be accepted", displaced, row.Code)
			}
			continue
		}
		dataRows++
		c := row.Character[0]
		if got := toCode(c); int(got) != row.Value {
			t.Errorf("%q encodes to %06b, register says %s", c, got, row.Code)
		}
		got, ok := fromCode(byte(row.Value))
		if !ok || got != c {
			t.Errorf("%s decodes to %q/%v, register says %q", row.Code, got, ok, c)
		}
		if !accepted(c) {
			t.Errorf("%q is a data character in the register table but not accepted", c)
		}
	}
	if dataRows != 59 || controlRows != 5 {
		t.Errorf("table has %d data rows and %d control rows, want 59 and 5", dataRows, controlRows)
	}
}

// TestAnnexAAnchors pins the rows that refute the two superseded assumptions:
// the plain-offset mapping, and the claim that all 64 code points are
// printable characters.
func TestAnnexAAnchors(t *testing.T) {
	for _, c := range []struct {
		code byte
		want byte
	}{{0x00, '@'}, {0x01, 'A'}, {0x10, 'P'}, {0x1A, 'Z'}, {0x20, ' '}, {0x30, '0'}, {0x39, '9'}, {0x3F, '?'}} {
		got, ok := fromCode(c.code)
		if !ok || got != c.want {
			t.Errorf("%06b = %q/%v, want %q", c.code, got, ok, c.want)
		}
	}
	// The five structural control positions, and the printable character each
	// one displaces (issue #8 Amendment 2, decision B1).
	for _, c := range []struct {
		code      byte
		name      string
		value     byte
		displaces byte
	}{
		{CodeGS, "GS", 0x1D, '^'},
		{CodeRS, "RS", 0x1E, '_'},
		{CodeEOT, "EOT", 0x04, '!'},
		{CodeFS, "FS", 0x1C, '#'},
		{CodeUS, "US", 0x1F, '$'},
	} {
		if _, ok := fromCode(c.code); ok {
			t.Errorf("%06b (%s) decoded as a data character", c.code, c.name)
		}
		if got := controls[c.code]; got.Name != c.name || got.Byte != c.value {
			t.Errorf("%06b = %+v, want %s/0x%02X", c.code, got, c.name, c.value)
		}
		if accepted(c.displaces) {
			t.Errorf("%q must not be encodable: %06b is %s", c.displaces, c.code, c.name)
		}
	}
}

// TestRepertoireIsComplete checks exactly which characters are encodable.
//
// The repertoire is ASCII 0x20-0x5F minus the five structural control
// positions, so it is 59 characters, not 64.
func TestRepertoireIsComplete(t *testing.T) {
	unencodable := map[byte]bool{'!': true, '#': true, '$': true, '^': true, '_': true}
	count := 0
	for c := 0; c < 256; c++ {
		want := c >= 0x20 && c <= 0x5F && !unencodable[byte(c)]
		if got := accepted(byte(c)); got != want {
			t.Errorf("accepted(0x%02X) = %v, want %v", c, got, want)
		}
		if want {
			count++
		}
	}
	if count != 59 {
		t.Errorf("repertoire has %d characters, want 59", count)
	}
	// '@' is a data character, not a pad marker.
	if !accepted('@') {
		t.Error("'@' must be accepted: it is 6-bit code 000000")
	}
}

// TestControlsAreNotData checks the structural channel: a control code is
// reported as a control and never appears in the decoded data characters
// (issue #8 Amendment 2, decision B3).
func TestControlsAreNotData(t *testing.T) {
	// "AB" GS "CD" EOT, eight codes, 48 bits, three whole words.
	codes := []byte{toCode('A'), toCode('B'), CodeGS, toCode('C'), toCode('D'), CodeEOT, toCode('E'), toCode('F')}
	bits := make([]byte, 6)
	for i, code := range codes {
		setBits(bits, i*6, 6, uint64(code))
	}
	text, err := DecodeText(bits, 48)
	if err != nil {
		t.Fatal(err)
	}
	if text.Characters != "ABCDEF" {
		t.Errorf("characters = %q, want ABCDEF", text.Characters)
	}
	if len(text.Controls) != 2 {
		t.Fatalf("controls = %+v, want two", text.Controls)
	}
	if text.Controls[0].Name != "GS" || text.Controls[0].Index != 2 {
		t.Errorf("first control = %+v, want GS at 2", text.Controls[0])
	}
	if text.Controls[1].Name != "EOT" || text.Controls[1].Index != 5 {
		t.Errorf("second control = %+v, want EOT at 5", text.Controls[1])
	}
	// The separator and terminator roles are established, but the layout they
	// imply for a 6-bit construct is not, so interpretation refuses rather
	// than acting on them (decision B2 tier 2).
	c, err := Recognise(AFITransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Interpret(c, text); !errors.Is(err, tdtcore.ErrUnresolvedSemantics) {
		t.Errorf("interpret error = %v, want ErrUnresolvedSemantics", err)
	}
	// RS, FS and US have no role at all, so they are a structural defect.
	for _, code := range []byte{CodeRS, CodeFS, CodeUS} {
		bad := Text{Characters: "JABC", Controls: []Control{{Code: code, Name: controls[code].Name}}}
		if _, _, err := Interpret(c, bad); !errors.Is(err, tdtcore.ErrFieldValue) {
			t.Errorf("%s: interpret error = %v, want a field error", controls[code].Name, err)
		}
	}
}

// TestUnencodableCharactersAreRefused checks the encoder refuses the five
// characters the control positions displace.
func TestUnencodableCharactersAreRefused(t *testing.T) {
	for _, c := range []string{"!", "#", "$", "^", "_"} {
		_, _, err := EncodeText("probe", "JABCDE"+c+"H")
		if !errors.Is(err, tdtcore.ErrFieldValue) {
			t.Errorf("%q: error = %v, want a field error", c, err)
		}
	}
}

// TestRecogniseDistinguishesRegistrationFromSupport checks the three outcomes
// of the recognition stage.
func TestRecogniseDistinguishesRegistrationFromSupport(t *testing.T) {
	for _, afi := range []uint8{AFITransportUnit, AFITransportUnitHazardous, AFI17363, AFI17363Historical} {
		if _, err := Recognise(afi); err != nil {
			t.Errorf("AFI 0x%02X: %v", afi, err)
		}
	}
	// Registered by the authority, outside this build's baseline. 0xB0 is in
	// this group, not the next one: it is registered to the 6C Toll Operators
	// Coalition.
	for _, afi := range []uint8{0xA1, 0xA3, 0xA5, 0x02, 0x92, 0xAC, 0xAD, 0xB0, 0xC0} {
		if _, err := Recognise(afi); !errors.Is(err, tdtcore.ErrUnsupportedScheme) {
			t.Errorf("registered but unsupported AFI 0x%02X: error = %v", afi, err)
		}
	}
	// Outside the ranges ISO/IEC 15961-3:2019 Table 1 makes assignable at all.
	for _, afi := range []uint8{0x10, 0x8F, 0xD0, 0xFF} {
		if _, err := Recognise(afi); !errors.Is(err, tdtcore.ErrUnrecognisedNonGS1) {
			t.Errorf("unassignable AFI 0x%02X: error = %v", afi, err)
		}
	}
}

// TestEncodingIsDispatchedNotDefaulted checks decision B9: an AFI whose
// registered compaction this build does not implement is refused, never read
// as 6-bit text.
func TestEncodingIsDispatchedNotDefaulted(t *testing.T) {
	bits, bitLen, err := EncodeText("probe", "JABCDEFG")
	if err != nil {
		t.Fatal(err)
	}
	byAFI := map[uint8]Construct{}
	for _, c := range Constructs() {
		byAFI[c.AFI] = c
	}
	for _, afi := range []uint8{0xAC, 0xAD, 0xA0, 0xB0} {
		c, ok := byAFI[afi]
		if !ok {
			t.Fatalf("AFI 0x%02X must be in the table", afi)
		}
		if c.Encoding == Compaction6Bit {
			t.Errorf("AFI 0x%02X must not be registered as 6-bit", afi)
		}
		if _, err := Decode(bits, bitLen, afi); errors.Is(err, tdtcore.ErrFieldValue) {
			t.Errorf("AFI 0x%02X: decoded as text instead of being refused", afi)
		}
	}
	// An AFI with no registered compaction at all is unresolved, not merely
	// unsupported: there is nothing to implement.
	c := byAFI[AFI17363]
	if c.Encoding != CompactionNone {
		t.Errorf("AFI 0x%02X encoding = %q, want none", AFI17363, c.Encoding)
	}
}

// TestSchemeMapping pins the AFI to scheme mapping and the 17360 naming.
func TestSchemeMapping(t *testing.T) {
	for afi, want := range map[uint8]identity.SchemeID{
		0xA2: SchemeTransportUnit,
		0xA7: SchemeTransportUnit,
		0xA9: Scheme17363,
		0xAA: Scheme17363,
	} {
		got, err := SchemeForAFI(afi)
		if err != nil {
			t.Fatalf("AFI 0x%02X: %v", afi, err)
		}
		if got != want {
			t.Errorf("AFI 0x%02X = %s, want %s", afi, got, want)
		}
	}
	if afi, _ := AFIForScheme(SchemeTransportUnit); afi != AFITransportUnit {
		t.Errorf("preferred transport unit AFI = 0x%02X", afi)
	}
}

// TestRegisteredDataIdentifiers checks the closed set is enforced, including
// longest-match, rather than a general syntax being parsed.
func TestRegisteredDataIdentifiers(t *testing.T) {
	c, err := Recognise(AFITransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	for _, di := range []string{"J", "1J", "2J", "3J", "4J", "5J", "6J"} {
		if !c.Allows(di) {
			t.Errorf("%s is registered for 0xA2 but not allowed", di)
		}
	}
	// Syntactically plausible, not registered for this AFI.
	for _, di := range []string{"25S", "7B", "1B", "9J", "K"} {
		if c.Allows(di) {
			t.Errorf("%s is not registered for 0xA2 but was allowed", di)
		}
	}
	if di, body, ok := splitRegisteredDI(c, "1JABCDE"); !ok || di != "1J" || body != "ABCDE" {
		t.Errorf("longest match = %q/%q/%v, want 1J/ABCDE/true", di, body, ok)
	}
	if di, body, ok := splitRegisteredDI(c, "JABCDEF"); !ok || di != "J" || body != "ABCDEF" {
		t.Errorf("short match = %q/%q/%v", di, body, ok)
	}
	if _, _, ok := splitRegisteredDI(c, "25SABCDE"); ok {
		t.Error("an unregistered data identifier matched")
	}
}

func transportUnit(t *testing.T, di, body string) identity.Value {
	t.Helper()
	v, err := identity.New(identity.AuthorityISO15459, identity.KeyTypeTransportUnit, []identity.Component{
		{Name: identity.DataIdentifier, Value: di},
		{Name: identity.ISOIdentifier, Value: body},
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestEncodeRefusesPadding checks that a payload needing pad bits is refused
// instead of padded with a guessed value.
func TestEncodeRefusesPadding(t *testing.T) {
	// Eight characters is 48 bits, three whole words: no padding needed.
	if _, err := Encode(transportUnit(t, "J", "ABCDEFG"), SchemeTransportUnit); err != nil {
		t.Fatalf("aligned payload: %v", err)
	}
	// Anything else would need a pad value we cannot cite.
	for _, body := range []string{"ABCDEF", "ABCDEFGH", "A"} {
		_, err := Encode(transportUnit(t, "J", body), SchemeTransportUnit)
		if !errors.Is(err, tdtcore.ErrUnresolvedSemantics) {
			t.Errorf("body %q: error = %v, want ErrUnresolvedSemantics", body, err)
		}
	}
}

// TestDecodeTrimsNothing checks that no trailing character is stripped: which
// characters a writer intended as padding is not established.
func TestDecodeTrimsNothing(t *testing.T) {
	enc, err := Encode(transportUnit(t, "J", "ABCDEFG"), SchemeTransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	bits, bitLen := enc.Encoding.Bits()
	text, err := DecodeText(bits, bitLen)
	if err != nil {
		t.Fatal(err)
	}
	if text.Characters != "JABCDEFG" {
		t.Errorf("characters = %q, want JABCDEFG", text.Characters)
	}
	if text.ResidueBits != 0 {
		t.Errorf("residue = %d, want 0", text.ResidueBits)
	}
	if len(text.Controls) != 0 {
		t.Errorf("controls = %+v, want none", text.Controls)
	}

	// '@' is 6-bit code 000000 and is an ordinary data character, not a pad
	// marker. That is a property of the codec. It is separately true that
	// ISO/IEC 15459-1:2014 5.3 does not permit it inside a transport unit
	// identity, which is a property of the application standard; the two
	// stages are tested apart because they fail for different reasons.
	raw, n, err := EncodeText("probe", "JAB@DEFG")
	if err != nil {
		t.Fatalf("'@' must be encodable: %v", err)
	}
	at, err := DecodeText(raw, n)
	if err != nil {
		t.Fatal(err)
	}
	if at.Characters != "JAB@DEFG" {
		t.Errorf("characters = %q, want JAB@DEFG", at.Characters)
	}
	if len(at.Controls) != 0 {
		t.Errorf("'@' must decode as data, got controls %+v", at.Controls)
	}
	construct, err := Recognise(AFITransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Interpret(construct, at); !errors.Is(err, tdtcore.ErrFieldValue) {
		t.Errorf("interpret error = %v, want a 15459-1 5.3 field error", err)
	}
}

// TestResidueBitsAreUnresolved checks that a UII which does not end on a
// character boundary is refused at interpretation rather than guessed at.
func TestResidueBitsAreUnresolved(t *testing.T) {
	enc, err := Encode(transportUnit(t, "J", "ABCDEFG"), SchemeTransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	bits, _ := enc.Encoding.Bits()
	// 64 bits holds ten complete characters and four residue bits.
	padded := make([]byte, 8)
	copy(padded, bits)
	text, err := DecodeText(padded, 64)
	if err != nil {
		t.Fatal(err)
	}
	if text.ResidueBits != 4 {
		t.Fatalf("residue = %d, want 4", text.ResidueBits)
	}
	construct, err := Recognise(AFITransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Interpret(construct, text); !errors.Is(err, tdtcore.ErrUnresolvedSemantics) {
		t.Errorf("error = %v, want ErrUnresolvedSemantics", err)
	}
	if _, err := Decode(padded, 64, AFITransportUnit); !errors.Is(err, tdtcore.ErrUnresolvedSemantics) {
		t.Errorf("decode error = %v, want ErrUnresolvedSemantics", err)
	}
}

// TestTransportUnitRoundTrip checks the one construct whose payload rules the
// register does establish.
func TestTransportUnitRoundTrip(t *testing.T) {
	// Sixteen characters is 96 bits, six whole words: no padding is needed.
	value := transportUnit(t, "1J", "UN043325711MH8")
	enc, err := Encode(value, SchemeTransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	if enc.Encoding.Control.AFI != AFITransportUnit || !enc.Encoding.Control.Toggle {
		t.Errorf("control = %+v", enc.Encoding.Control)
	}
	if enc.Encoding.Control.HasFilter || enc.Encoding.Control.HasPartition {
		t.Error("an ISO encoding must not carry GS1 control fields")
	}
	bits, bitLen := enc.Encoding.Bits()
	back, err := Decode(bits, bitLen, AFITransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Identity.Equal(value) || back.Identity.Key() != value.Key() {
		t.Errorf("round trip changed the identity: %s", back.Identity.Describe())
	}
	if back.Identity.Authority() != identity.AuthorityISO15459 {
		t.Errorf("authority = %s, want iso15459", back.Identity.Authority())
	}
	for c := range back.Identity.Components() {
		if c.Name == identity.FilterValue || c.Name == identity.Partition {
			t.Errorf("identity carries encoding-control component %s", c.Name)
		}
	}
}

// TestUnrecognisedDataIdentifierIsUnsupported checks decision B6: a well
// formed data identifier outside the recognised set is unsupported, not
// invalid. The register edition that published the closed sets has been
// superseded by one that removed the table.
func TestUnrecognisedDataIdentifierIsUnsupported(t *testing.T) {
	if _, err := Encode(transportUnit(t, "25S", "ABCDE"), SchemeTransportUnit); !errors.Is(err, tdtcore.ErrUnsupportedScheme) {
		t.Errorf("encode error = %v, want ErrUnsupportedScheme", err)
	}
	construct, err := Recognise(AFITransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Interpret(construct, Text{Characters: "QQABCDEF"}); !errors.Is(err, tdtcore.ErrUnsupportedScheme) {
		t.Errorf("interpret error = %v, want ErrUnsupportedScheme", err)
	}
	// It must specifically NOT be reported as a field validation failure.
	if _, _, err := Interpret(construct, Text{Characters: "QQABCDEF"}); errors.Is(err, tdtcore.ErrFieldValue) {
		t.Error("an unrecognised data identifier must not be reported as invalid")
	}
}

// TestAuthorityComesFromTheDataIdentifier checks decision B5.
func TestAuthorityComesFromTheDataIdentifier(t *testing.T) {
	for _, di := range []string{"J", "1J", "2J", "3J", "4J", "5J", "6J", "25S", "25T", "25B", "5R"} {
		a, ok := AuthorityForDI(di)
		if !ok || a != identity.AuthorityISO15459 {
			t.Errorf("%s: authority = %s/%v, want iso15459", di, a, ok)
		}
	}
	if a, ok := AuthorityForDI("7B"); !ok || a != identity.AuthorityISO6346 {
		t.Errorf("7B: authority = %s/%v, want iso6346", a, ok)
	}
	// Data identifiers whose numbering authority is outside the baseline are
	// absent rather than given a wrong default.
	for _, di := range []string{"I", "1B", "7J"} {
		if a, ok := AuthorityForDI(di); ok {
			t.Errorf("%s: authority = %s, want none", di, a)
		}
	}
}

// TestISO15459IdentityLimits checks decision M10: the length and character set
// rules of ISO/IEC 15459-1:2014 clause 5.
func TestISO15459IdentityLimits(t *testing.T) {
	construct, err := Recognise(AFITransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	// 5.2: at most 50 characters, qualifier included.
	long := "1J" + strings.Repeat("A", 49)
	if _, _, err := Interpret(construct, Text{Characters: long}); !errors.Is(err, tdtcore.ErrLengthMismatch) {
		t.Errorf("over-long identity: error = %v, want ErrLengthMismatch", err)
	}
	ok48 := "1J" + strings.Repeat("A", 48)
	if _, _, err := Interpret(construct, Text{Characters: ok48}); err != nil {
		t.Errorf("50-character identity: %v", err)
	}
	// 5.3: upper case alphabetic and numeric only.
	for _, body := range []string{"abc", "A-B", "A B", "A.B", "A/B"} {
		if _, _, err := Interpret(construct, Text{Characters: "1J" + body}); !errors.Is(err, tdtcore.ErrFieldValue) {
			t.Errorf("body %q: error = %v, want a field error", body, err)
		}
	}
	if _, _, err := Interpret(construct, Text{Characters: "1JUN0439"}); err != nil {
		t.Errorf("valid identity: %v", err)
	}
	// The encoder applies the same rules.
	if _, err := Encode(transportUnit(t, "1J", "abcdef"), SchemeTransportUnit); !errors.Is(err, tdtcore.ErrFieldValue) {
		t.Error("the encoder must apply the 15459-1 5.3 character set")
	}
}

// TestIdentityIsNotDecomposedBeyondTheQualifier checks gate G10: the boundary
// between the company identifying number and the serial number is defined per
// Issuing Agency and is not decidable from the encoded string.
func TestIdentityIsNotDecomposedBeyondTheQualifier(t *testing.T) {
	v := transportUnit(t, "1J", "UN043325711MH8")
	order := identity.ComponentOrder(identity.KeyTypeTransportUnit)
	if len(order) != 2 || order[0] != identity.DataIdentifier || order[1] != identity.ISOIdentifier {
		t.Fatalf("component order = %v, want the qualifier and the remainder only", order)
	}
	body, ok := v.Component(identity.ISOIdentifier)
	if !ok || body.Value != "UN043325711MH8" {
		t.Errorf("identifier = %+v, want the undivided remainder", body)
	}
}

// TestISO17363IsRecognisedButNotPromoted is decision A7 in test form.
func TestISO17363IsRecognisedButNotPromoted(t *testing.T) {
	construct, err := Recognise(AFI17363)
	if err != nil {
		t.Fatalf("0xA9 must be recognised: %v", err)
	}
	if len(construct.DataIdentifiers) != 0 || construct.Encoding != CompactionNone {
		t.Errorf("the register assigns ISO 17363 no construct, got %+v", construct)
	}
	// The exported codec still works for a caller who knows their own layout.
	bits, bitLen, err := EncodeText("probe", "7BCSQU30")
	if err != nil {
		t.Fatal(err)
	}
	text, err := DecodeText(bits, bitLen)
	if err != nil {
		t.Fatalf("the exported codec must still work: %v", err)
	}
	if text.Characters != "7BCSQU30" {
		t.Errorf("characters = %q", text.Characters)
	}
	// Translation does not. Reading AFI 0xA9 as 6-bit text would be an
	// analogy with the ISO/IEC 17360 family, not an inference from evidence,
	// so decoding refuses before the character stage (decisions B8 and B9).
	if _, _, err := Interpret(construct, text); !errors.Is(err, tdtcore.ErrUnresolvedSemantics) {
		t.Errorf("interpret error = %v, want ErrUnresolvedSemantics", err)
	}
	if _, err := Decode(bits, bitLen, AFI17363); !errors.Is(err, tdtcore.ErrUnresolvedSemantics) {
		t.Errorf("decode error = %v, want ErrUnresolvedSemantics", err)
	}
	value, err := InterpretContainerCode("CSQU3054383")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(value, Scheme17363); !errors.Is(err, tdtcore.ErrUnresolvedSemantics) {
		t.Errorf("encode error = %v, want ErrUnresolvedSemantics", err)
	}
}

// TestContainerCodeInterpreter covers the ISO 6346 rules, which are known
// independently of how a tag carries them.
func TestContainerCodeInterpreter(t *testing.T) {
	value, err := InterpretContainerCode("CSQU3054383")
	if err != nil {
		t.Fatal(err)
	}
	if value.Authority() != identity.AuthorityISO6346 || value.KeyType() != identity.KeyTypeFreightContainer {
		t.Errorf("shape = %s/%s", value.Authority(), value.KeyType())
	}
	owner, _ := value.Component(identity.OwnerCode)
	if owner.Value != "CSQ" {
		t.Errorf("owner = %q", owner.Value)
	}
	for _, c := range []struct {
		code  string
		errIs error
	}{
		{"CSQU3054384", tdtcore.ErrCheckDigit},
		{"CSQX3054383", tdtcore.ErrFieldValue},
		{"CS1U3054383", tdtcore.ErrFieldValue},
		{"CSQU30543X3", tdtcore.ErrFieldValue},
		{"CSQU30543", tdtcore.ErrFieldValue},
	} {
		if _, err := InterpretContainerCode(c.code); !errors.Is(err, c.errIs) {
			t.Errorf("%s: error = %v, want %v", c.code, err, c.errIs)
		}
	}
}

// TestCheckDigit6346 uses the published worked example.
func TestCheckDigit6346(t *testing.T) {
	for code, want := range map[string]string{
		"CSQU305438": "3", // ISO 6346 clause 4.3 worked example: CSQU 305438 3
		"MSCU665863": "3",
		"TEXU123456": "9",
	} {
		got, err := CheckDigit6346(code)
		if err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		if got != want {
			t.Errorf("%s: check digit = %s, want %s", code, got, want)
		}
	}
	for c, v := range map[byte]int{'A': 10, 'B': 12, 'K': 21, 'L': 23, 'U': 32, 'V': 34, 'Z': 38} {
		got, err := characterValue6346(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != v {
			t.Errorf("%c = %d, want %d", c, got, v)
		}
	}
}

// TestDisplayIsNotAContract documents that ISO rendering is diagnostic only.
func TestDisplayIsNotAContract(t *testing.T) {
	got := Display(transportUnit(t, "J", "ABCDEFG"))
	if got == "" || strings.HasPrefix(got, "urn:") {
		t.Fatalf("display = %q", got)
	}
}

// TestRegistryGenerationIsReproducible keeps the generated construct table a
// pure function of the transcribed register data.
func TestRegistryGenerationIsReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generation in short mode")
	}
	want, err := os.ReadFile("registry_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "tdt", "iso")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("registry.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "registry.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./internal/tdt/iso/cmd/genafi", "-root", root)
	cmd.Dir = filepath.Join("..", "..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("genafi: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "registry_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("registry_gen.go differs from a fresh generation; run go generate ./internal/tdt/...")
	}
}

// TestRegistrySourceIsRecorded checks provenance survives generation.
func TestRegistrySourceIsRecorded(t *testing.T) {
	if RegisterSource.AssignmentsEdition != "2025-11-25" || RegisterSource.ConstructsEdition != "2023-09-25" {
		t.Errorf("source = %+v", RegisterSource)
	}
	if !strings.Contains(RegisterSource.Document, "15961-2") {
		t.Errorf("document = %q", RegisterSource.Document)
	}
}

// TestRegisterVectors cross-checks the generated construct table and the
// ISO 6346 check digit against the transcribed vector file, so the generated
// table and the vectors cannot drift apart unnoticed.
func TestRegisterVectors(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "testdata", "vectors", "iso.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		RegisteredAFIs []struct {
			AFI             string   `json:"afi"`
			Role            string   `json:"role"`
			Supported       bool     `json:"supported"`
			DataIdentifiers []string `json:"dataIdentifiers"`
		} `json:"registeredAFIs"`
		UnregisteredAFIs []string `json:"unregisteredAFIs"`
		ISO6346          struct {
			Vectors []struct {
				Code       string `json:"code"`
				CheckDigit string `json:"checkDigit"`
			} `json:"vectors"`
		} `json:"iso6346"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}

	byAFI := map[uint8]Construct{}
	for _, c := range Constructs() {
		byAFI[c.AFI] = c
	}
	for _, want := range doc.RegisteredAFIs {
		afi := parseAFI(t, want.AFI)
		got, ok := byAFI[afi]
		if !ok {
			t.Errorf("AFI %s is in the vectors but not in the generated table", want.AFI)
			continue
		}
		if got.Supported != want.Supported {
			t.Errorf("AFI %s: supported = %v, vectors say %v", want.AFI, got.Supported, want.Supported)
		}
		if got.Role != want.Role {
			t.Errorf("AFI %s: role = %q, vectors say %q", want.AFI, got.Role, want.Role)
		}
		if len(got.DataIdentifiers) != len(want.DataIdentifiers) {
			t.Errorf("AFI %s: %d data identifiers, vectors say %d", want.AFI, len(got.DataIdentifiers), len(want.DataIdentifiers))
			continue
		}
		for _, di := range want.DataIdentifiers {
			if !got.Allows(di) {
				t.Errorf("AFI %s: %s is in the vectors but not registered", want.AFI, di)
			}
		}
	}
	for _, text := range doc.UnregisteredAFIs {
		if _, ok := byAFI[parseAFI(t, text)]; ok {
			t.Errorf("AFI %s is listed as unregistered but appears in the table", text)
		}
	}
	for _, v := range doc.ISO6346.Vectors {
		got, err := CheckDigit6346(v.Code)
		if err != nil {
			t.Fatalf("%s: %v", v.Code, err)
		}
		if got != v.CheckDigit {
			t.Errorf("%s: check digit = %s, vectors say %s", v.Code, got, v.CheckDigit)
		}
	}
}

func parseAFI(t *testing.T, text string) uint8 {
	t.Helper()
	v, err := strconv.ParseUint(strings.TrimPrefix(text, "0x"), 16, 8)
	if err != nil {
		t.Fatalf("AFI %q: %v", text, err)
	}
	return uint8(v)
}
