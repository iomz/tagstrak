package tdtcore

import (
	"github.com/iomz/tagstrak/v2/internal/identity"
)

// Control holds encoding-control fields: the parts of a tag that say how an
// identity was written rather than what it identifies.
//
// Two tags whose Control differs but whose identity components agree denote
// the same thing, which is why none of these fields reaches identity.Value.
type Control struct {
	// FilterValue is the EPC filter, valid when HasFilter is set.
	FilterValue uint8
	HasFilter   bool
	// Partition selects the company prefix split, valid when HasPartition is set.
	Partition    uint8
	HasPartition bool
	// GCPLength is the GS1 Company Prefix digit count, zero for non-GS1 data.
	GCPLength int
	// DataIdentifier is the ANS MH10.8.2 data identifier carried by ISO data.
	DataIdentifier string
	// ResidueBits counts the bits after the last complete character of a
	// variable-length character encoding. Whether they hold a truncated
	// terminator or nothing is not established for the 6-bit constructs, so
	// they are reported rather than interpreted. See issue #8 Amendment 2,
	// decision B2, and issue #19 gates U1 and U10.
	ResidueBits int
	// Toggle is Gen2 PC bit 17h: false selects GS1 EPC data, true non-GS1.
	Toggle bool
	// AFI is the ISO/IEC 15961-2 application family identifier, valid when
	// HasAFI is set. It is only meaningful when Toggle is true.
	AFI    uint8
	HasAFI bool
}

// Encoding is the physical representation of an identity on a tag.
type Encoding struct {
	// Scheme is the encoding label, such as "sgtin-96" or "iso17363".
	Scheme identity.SchemeID
	// TagLength is the encoding's nominal length in bits.
	TagLength int
	// BitLen is the number of significant bits in Bits.
	BitLen int
	// Control carries the encoding-control fields.
	Control Control

	bits   []byte
	fields []identity.Component
}

// NewEncoding builds an Encoding, copying the caller's bits and fields.
func NewEncoding(scheme identity.SchemeID, tagLength, bitLen int, control Control, bits []byte, fields []identity.Component) Encoding {
	return Encoding{
		Scheme:    scheme,
		TagLength: tagLength,
		BitLen:    bitLen,
		Control:   control,
		bits:      append([]byte(nil), bits...),
		fields:    append([]identity.Component(nil), fields...),
	}
}

// Bits returns a copy of the encoded bits and their significant bit length.
func (e Encoding) Bits() ([]byte, int) {
	return append([]byte(nil), e.bits...), e.BitLen
}

// RawBits returns the encoded bits without copying. Callers must not modify
// the result; it exists so matching can avoid an allocation per tag.
func (e Encoding) RawBits() []byte { return e.bits }

// Fields returns the encoding-level component vector, in the scheme's own
// order, including encoding-control fields such as the EPC filter value.
// Encoding-level patterns are matched against exactly this vector.
func (e Encoding) Fields() []identity.Component {
	return append([]identity.Component(nil), e.fields...)
}

// RawFields returns the encoding-level component vector without copying.
// Callers must not modify the result.
func (e Encoding) RawFields() []identity.Component { return e.fields }

// IsZero reports whether the encoding is unset.
func (e Encoding) IsZero() bool { return e.Scheme == "" && e.BitLen == 0 }

// Translation is the result of translating tag data: the canonical identity
// and the encoding it was carried in, bound together but never merged.
type Translation struct {
	Identity identity.Value
	Encoding Encoding
}

// IdentityFields returns the identity component vector, in canonical order.
// Identity-level patterns are matched against exactly this vector.
func (t Translation) IdentityFields() []identity.Component {
	out := make([]identity.Component, 0, t.Identity.Len())
	for c := range t.Identity.Components() {
		out = append(out, c)
	}
	return out
}

// EncodingFields returns the encoding component vector.
func (t Translation) EncodingFields() []identity.Component { return t.Encoding.Fields() }

// Bits returns the encoded bits and their significant bit length.
func (t Translation) Bits() ([]byte, int) { return t.Encoding.Bits() }

// Match reports whether the translation satisfies a compiled pattern, at
// whichever level the pattern constrains.
//
// This is the reference implementation of match semantics for whole
// translations. Optimised matchers must agree with it on the conformance
// corpus in internal/tdt.
func (t Translation) Match(p identity.Pattern) bool {
	switch p.Level() {
	case identity.LevelIdentity:
		return p.MatchValue(t.Identity)
	case identity.LevelEncoding:
		if p.Scheme() != t.Encoding.Scheme {
			return false
		}
		return p.MatchComponents(t.Encoding.RawFields())
	default:
		return false
	}
}
