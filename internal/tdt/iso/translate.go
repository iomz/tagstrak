package iso

import (
	"fmt"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

// Decode runs the three stages over non-GS1 tag data: recognise the construct
// from the AFI, decode the character stream, then interpret the payload.
//
// Recognition and decoding failures are reported as such. A construct whose
// payload rules are not established fails only the third stage, with
// ErrUnresolvedSemantics.
//
// Character decoding dispatches on the compaction the register records for the
// AFI. The 6-bit codec is never a fallback: an AFI whose registered compaction
// this build does not implement, and an AFI for which the register records no
// compaction at all, are both refused rather than read as 6-bit text (issue #8
// Amendment 2, decision B9).
func Decode(bits []byte, bitLen int, afi uint8) (tdtcore.Translation, error) {
	var out tdtcore.Translation
	construct, err := Recognise(afi)
	if err != nil {
		return out, err
	}
	scheme, err := construct.Scheme()
	if err != nil {
		return out, err
	}
	text, err := decodeFor(construct, scheme, bits, bitLen)
	if err != nil {
		return out, err
	}
	value, di, err := Interpret(construct, text)
	if err != nil {
		return out, err
	}

	control := tdtcore.Control{
		DataIdentifier: di,
		ResidueBits:    text.ResidueBits,
		Toggle:         true,
		AFI:            afi,
		HasAFI:         true,
	}
	size := (bitLen + 7) / 8
	raw := make([]byte, size)
	copy(raw, bits[:size])
	fields := make([]identity.Component, 0, value.Len())
	for c := range value.Components() {
		fields = append(fields, c)
	}
	out.Identity = value
	out.Encoding = tdtcore.NewEncoding(scheme, bitLen, bitLen, control, raw, fields)
	return out, nil
}

// decodeFor selects the character decoder the construct's registered
// compaction calls for.
//
// AFI 0xA9 and 0xAA carry no registered compaction at all. Reading them as
// 6-bit text would be an analogy with the ISO/IEC 17360 family rather than an
// inference from the evidence, so it is refused here, at the decoding stage,
// rather than silently attempted (issue #8 Amendment 2, decisions B8 and B9).
// DecodeText stays exported for a caller who knows their own tag layout.
func decodeFor(c Construct, scheme identity.SchemeID, bits []byte, bitLen int) (Text, error) {
	switch c.Encoding {
	case Compaction6Bit:
		return DecodeText(bits, bitLen)
	case CompactionNone:
		return Text{}, fmt.Errorf("%w: the register assigns AFI 0x%02X (%s) no compaction method, so its UII encoding is not established",
			tdtcore.ErrUnresolvedSemantics, c.AFI, scheme)
	default:
		return Text{}, fmt.Errorf("%w: AFI 0x%02X uses %s compaction, which this build does not implement",
			tdtcore.ErrUnsupportedScheme, c.AFI, c.Encoding)
	}
}

// Encode writes an ISO identity back onto a tag.
//
// It refuses any payload that would need padding to reach word alignment: the
// pad convention is not established, and choosing one would be inventing wire
// semantics (issue #8 Amendment 1, decision A8).
func Encode(v identity.Value, scheme identity.SchemeID) (tdtcore.Translation, error) {
	var out tdtcore.Translation
	afi, err := AFIForScheme(scheme)
	if err != nil {
		return out, err
	}
	construct, err := Recognise(afi)
	if err != nil {
		return out, err
	}
	if construct.Encoding != Compaction6Bit {
		return out, fmt.Errorf("%w: AFI 0x%02X has no 6-bit compaction registered, so %s cannot be written",
			tdtcore.ErrUnresolvedSemantics, afi, scheme)
	}
	if v.KeyType() != identity.KeyTypeTransportUnit {
		return out, fmt.Errorf("%w: %s cannot carry %s", tdtcore.ErrCannotEncode, scheme, v.KeyType())
	}

	di, ok := v.Component(identity.DataIdentifier)
	// Writing is restricted to the recognised data identifier set. That is a
	// deliberately conservative choice about what this build emits, not a
	// claim that an unrecognised identifier is invalid (issue #8 Amendment 2,
	// decision B6).
	if !ok || !construct.Allows(di.Value) {
		return out, fmt.Errorf("%w: %s writes only the recognised data identifiers %s, not %q",
			tdtcore.ErrUnsupportedScheme, scheme, strings.Join(registeredDIs(construct), ", "), di.Value)
	}
	authority, ok := AuthorityForDI(di.Value)
	if !ok || v.Authority() != authority {
		return out, fmt.Errorf("%w: data identifier %q qualifies an identity under %s, not %s",
			tdtcore.ErrCannotEncode, di.Value, authority, v.Authority())
	}
	body, ok := v.Component(identity.ISOIdentifier)
	if !ok {
		return out, tdtcore.Field(string(scheme), "identifier", "empty", "", "ISO/IEC 15459-1:2014 clause 5")
	}
	if err := checkISO15459Identity(scheme, di.Value, body.Value); err != nil {
		return out, err
	}
	text := di.Value + body.Value
	// Re-read the text the way a decoder would, so a value that cannot be
	// written back unambiguously is refused rather than written.
	if gotDI, gotBody, ok := splitRegisteredDI(construct, text); !ok || gotDI != di.Value || gotBody != body.Value {
		return out, tdtcore.Field(string(scheme), "di", "ambiguous", text,
			"ISO/IEC 15961-2 data constructs register")
	}

	bits, bitLen, err := EncodeText(string(scheme), text)
	if err != nil {
		return out, err
	}
	control := tdtcore.Control{
		DataIdentifier: di.Value,
		ResidueBits:    0,
		Toggle:         true,
		AFI:            afi,
		HasAFI:         true,
	}
	fields := make([]identity.Component, 0, v.Len())
	for c := range v.Components() {
		fields = append(fields, c)
	}
	out.Identity = v
	out.Encoding = tdtcore.NewEncoding(scheme, bitLen, bitLen, control, bits, fields)
	return out, nil
}

// Display renders a diagnostic form of an ISO identity for logs and CLI use.
//
// It is not canonical, not stable across versions, never parsed back, and
// never accepted as translator input. ISO publishes no URN form for these
// identities, and tagstrak deliberately does not invent one.
func Display(v identity.Value) string {
	switch v.KeyType() {
	case identity.KeyTypeFreightContainer, identity.KeyTypeTransportUnit:
	default:
		return v.Describe()
	}
	var b strings.Builder
	for i := 0; i < v.Len(); i++ {
		if i == 1 {
			b.WriteByte(' ')
		}
		b.WriteString(v.At(i).Value)
	}
	return b.String()
}
