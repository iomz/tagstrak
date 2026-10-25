// Package tdt is the tag data translation boundary: it turns tag bits into a
// canonical identity plus the encoding that carried it, and compiles
// identifier patterns into immutable matcher inputs.
//
// # Supported standards
//
// GS1 EPC Tag Data Standard 2.2, schemes SGTIN-96, SSCC-96, GIAI-96 and
// GRAI-96. Field layouts, numeric ranges and URI padding are generated from
// ratified GS1 Tag Data Translation artefacts pinned by commit and SHA-256;
// see SourceInfo and internal/tdt/tdtdef.
//
// ISO/IEC 17360 transport units (AFI 0xA2 and 0xA7), recognised through the
// Gen2 PC numbering-system toggle and the ISO/IEC 15961-2 Application Family
// Identifier register. Their identities are ISO/IEC 15459 identities,
// qualified by an ANS MH10.8.2 data identifier. ISO 17363 (AFI 0xA9 and 0xAA)
// is recognised but deliberately not decoded or promoted: its Gen2 carriage is
// not established. See internal/tdt/iso and issue #8 Amendment 2, which
// reconciles this package against the standards evidence baseline in issue
// #19.
//
// # Domains
//
// A Translation binds three things without merging them: the canonical
// identity (identity.Value), the representation-level component vector, and
// the physical encoding. Encoding-control fields — filter value, partition,
// tag length, PC toggle, AFI, residue bits — live only in the encoding.
// Encoding an identity is parameterised, because an identity does not say
// which filter value or tag length to write. Decoding is total for the GS1
// baseline; for ISO data it stops at whichever stage the published evidence
// stops supporting, and reports that stage's error.
//
// # Limitations
//
//   - GS1 element strings, GS1 AI JSON and GS1 Digital Link are out of scope
//     for v2.0.0 in either direction. Parsing them needs the GS1 Company
//     Prefix length table, which is deliberately not vendored.
//   - Encodings are never substituted: an identity that does not fit the
//     requested scheme returns an error rather than being promoted to a
//     longer one.
//   - Pattern syntax is GS1's. ISO patterns are built programmatically with
//     identity.NewPattern, because ISO publishes no pattern grammar and
//     tagstrak does not invent one.
//   - The ISO 6-bit separator and terminator are established: GS separates
//     data identifiers and EOT terminates the message. The LAYOUT they imply
//     for a 6-bit construct is not. The complete/incomplete-EOT rule is
//     normative only for the AFI 0xAD binary bitstream, and extending it to
//     the 6-bit constructs is Derived, as are the packing order and the status
//     of residue bits. So a decoded stream reports controls and residue rather
//     than acting on them, and writing a payload that would need a layout
//     decision is refused.
//   - ISO 17363 tags are recognised but not decoded through this boundary and
//     never promoted to a structured identity. The register assigns AFI 0xA9
//     and 0xAA no compaction method, ISO/IEC 15962 covers only ISO 17364,
//     17365, 17366 and 17367 in its data identifier direct encoding clauses,
//     and ISO 17363's own air interface is 433 MHz or 2,45 GHz rather than
//     ISO/IEC 18000-63. The ISO 6346 container code itself is well defined and
//     is available as iso.InterpretContainerCode for a caller who knows their
//     own tag layout.
//   - A Gen2 PC word with XI asserted counts one or two XPC words in its
//     length field. This build does not read XPC, so such input is refused
//     rather than having 16 or 32 bits of XPC consumed as identifier data.
//   - Translation performs no network access and consults no registry.
package tdt

import (
	"fmt"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/gs1epc"
	"github.com/iomz/tagstrak/v2/internal/tdt/iso"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtdef"
)

// Re-exported translation values and error taxonomy, so consumers depend on
// this boundary rather than on the packages behind it.
type (
	// Translation is a canonical identity bound to the encoding that carried it.
	Translation = tdtcore.Translation
	// Encoding is the physical representation of an identity on a tag.
	Encoding = tdtcore.Encoding
	// Control holds encoding-control fields.
	Control = tdtcore.Control
	// FieldError names the field and rule that rejected a value.
	FieldError = tdtcore.FieldError
)

// Error classes. Use errors.Is to classify a translation failure.
var (
	ErrUnknownHeader       = tdtcore.ErrUnknownHeader
	ErrUnsupportedScheme   = tdtcore.ErrUnsupportedScheme
	ErrUnrecognisedNonGS1  = tdtcore.ErrUnrecognisedNonGS1
	ErrAmbiguousEncoding   = tdtcore.ErrAmbiguousEncoding
	ErrTruncated           = tdtcore.ErrTruncated
	ErrReservedBits        = tdtcore.ErrReservedBits
	ErrUnknownPartition    = tdtcore.ErrUnknownPartition
	ErrMalformedURI        = tdtcore.ErrMalformedURI
	ErrMalformedPattern    = tdtcore.ErrMalformedPattern
	ErrFieldValue          = tdtcore.ErrFieldValue
	ErrLengthMismatch      = tdtcore.ErrLengthMismatch
	ErrUnresolvedSemantics = tdtcore.ErrUnresolvedSemantics
	ErrXPCPresent          = tdtcore.ErrXPCPresent
	ErrCheckDigit          = tdtcore.ErrCheckDigit
	ErrCannotEncode        = tdtcore.ErrCannotEncode
)

// Gen2 protocol-control bits this package interprets.
//
// The PC word carries L in bits 10h-14h, RUM in 15h, XI in 16h, the
// numbering-system toggle T in 17h, and the AFI in 18h-1Fh when T is set.
// Bit 15h was named UMI before GS1 EPC Gen2 Release 3.0; see Release 3.0.1,
// section 6.3.2.1.2.2, Table 6-19 and Table 6-20.
const (
	pcToggleMask  = 0x0100
	pcXIMask      = 0x0200
	pcAFIMask     = 0x00FF
	pcLengthMask  = 0xF800
	pcLengthShift = 11
)

// Format names the data format carried in a tag's UII/EPC memory.
type Format uint8

// Data formats.
const (
	// FormatUnknown means the format must be taken from the PC word.
	FormatUnknown Format = iota
	// FormatGS1 is GS1 EPC data, PC toggle clear.
	FormatGS1
	// FormatISO is non-GS1 data described by an AFI, PC toggle set.
	FormatISO
)

// Input is the translation boundary's input.
//
// It deliberately does not take an application tag type: keeping this struct
// narrow is what lets the translation packages stay free of application
// dependencies. Callers adapt their own observation types to it.
type Input struct {
	// Bits is the UII/EPC memory content, most significant bit first.
	Bits []byte
	// BitLen is the number of significant bits in Bits. Zero means "derive it
	// from the PC length field", and failing that, all of Bits.
	BitLen int
	// PC is the Gen2 protocol-control word, valid when HasPC is set.
	PC    uint16
	HasPC bool
	// AFI overrides the PC's AFI, for callers that obtained it separately.
	AFI    uint8
	HasAFI bool
}

// Option configures a Translator.
type Option func(*Translator)

// WithDefaultFormat sets the format to assume when a tag arrives without a PC
// word. Without it, such input returns ErrAmbiguousEncoding rather than a
// guess.
func WithDefaultFormat(f Format) Option {
	return func(t *Translator) { t.defaultFormat = f }
}

// Translator translates tag data. It holds no mutable state and is safe for
// concurrent use.
type Translator struct {
	defaultFormat Format
}

// New builds a Translator.
func New(opts ...Option) *Translator {
	t := &Translator{}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Translate turns tag data into a canonical identity and its encoding.
func (t *Translator) Translate(in Input) (Translation, error) {
	var out Translation
	bitLen := in.BitLen
	if in.HasPC {
		// L specifies "the length of the EPC that a Tag backscatters in
		// response to an ACK, in words" (GS1 EPC Gen2 Release 3.0.1,
		// 6.3.2.1.2.2). For a StoredPC that is the UII extent. For a
		// backscattered PacketPC with XI asserted it is not: the tag sends
		// XPC_W1, and XPC_W2 when XEB is also set, BEFORE the UII, and
		// increments L by one or two to cover them. Consuming L*16 bits as UII
		// would then eat 16 or 32 bits of XPC as identifier data, so it is
		// refused (issue #8 Amendment 2, decision B4).
		if in.PC&pcXIMask != 0 {
			return out, fmt.Errorf("%w: PC 0x%04X has XI asserted, so the length field covers one or two XPC words that were not supplied", ErrXPCPresent, in.PC)
		}
		declared := int((in.PC&pcLengthMask)>>pcLengthShift) * 16
		if declared == 0 {
			return out, fmt.Errorf("%w: the PC word declares a zero-length UII", ErrTruncated)
		}
		switch {
		case bitLen == 0:
			bitLen = declared
		case bitLen != declared:
			return out, fmt.Errorf("%w: PC declares %d bits, input reports %d", ErrLengthMismatch, declared, bitLen)
		}
	}
	if bitLen == 0 {
		bitLen = len(in.Bits) * 8
	}
	if bitLen <= 0 || len(in.Bits)*8 < bitLen {
		return out, fmt.Errorf("%w: %d bits in %d bytes", ErrTruncated, bitLen, len(in.Bits))
	}

	format := t.defaultFormat
	afi, hasAFI := in.AFI, in.HasAFI
	if in.HasPC {
		if in.PC&pcToggleMask != 0 {
			format = FormatISO
			if !hasAFI {
				afi, hasAFI = uint8(in.PC&pcAFIMask), true
			}
		} else {
			format = FormatGS1
		}
	}
	switch format {
	case FormatGS1:
		return gs1epc.Decode(in.Bits, bitLen)
	case FormatISO:
		if !hasAFI {
			return out, fmt.Errorf("%w: no AFI available", ErrUnrecognisedNonGS1)
		}
		return iso.Decode(in.Bits, bitLen, afi)
	default:
		return out, fmt.Errorf("%w: no PC word and no configured default", ErrAmbiguousEncoding)
	}
}

// CompilePattern compiles a pattern string into an immutable matcher input.
//
// Only GS1 pattern syntax exists: urn:epc:idpat: constrains the canonical
// identity and urn:epc:pat: constrains a tag encoding. Consumers receive
// compiled patterns and never parse identifier syntax themselves.
func CompilePattern(text string) (identity.Pattern, error) {
	return gs1epc.CompilePattern(strings.TrimSpace(text))
}

// PureIdentityURI renders the EPC Pure Identity URI of a GS1 identity.
func PureIdentityURI(v identity.Value) (string, error) { return gs1epc.PureIdentityURI(v) }

// TagURI renders the EPC Tag URI of a GS1 encoding.
func TagURI(e Encoding) (string, error) { return gs1epc.TagURI(e) }

// ParsePureIdentityURI parses an EPC Pure Identity URI.
func ParsePureIdentityURI(uri string) (identity.Value, error) {
	return gs1epc.ParsePureIdentityURI(uri)
}

// ParseTagURI parses an EPC Tag URI and re-encodes the bits it denotes.
func ParseTagURI(uri string) (Translation, error) { return gs1epc.ParseTagURI(uri) }

// EncodeGS1 writes a canonical GS1 identity into the requested tag encoding.
func EncodeGS1(v identity.Value, scheme identity.SchemeID, filter uint8) (Translation, error) {
	return gs1epc.Encode(v, gs1epc.EncodeParams{Scheme: scheme, FilterValue: filter})
}

// EncodeISO writes a canonical ISO identity into the requested tag encoding.
func EncodeISO(v identity.Value, scheme identity.SchemeID) (Translation, error) {
	return iso.Encode(v, scheme)
}

// ProtocolControl returns the Gen2 StoredPC word for an encoding: the UII word
// length, the numbering-system toggle and, for non-GS1 data, the AFI.
//
// This is a StoredPC. It is not a PacketPC: a tag that backscatters XPC words
// increments its transmitted length field to cover them, and that adjustment
// is the tag's, not ours (GS1 EPC Gen2 Release 3.0.1, 6.3.2.1.2.2). RUM and XI
// are computed by the tag and are left clear here.
func ProtocolControl(e Encoding) uint16 {
	words := (e.BitLen + 15) / 16
	pc := uint16(words) << pcLengthShift
	if e.Control.Toggle {
		pc |= pcToggleMask
		if e.Control.HasAFI {
			pc |= uint16(e.Control.AFI)
		}
	}
	return pc
}

// FieldSpec describes one identity field of a GS1 scheme option, for callers
// that build identities rather than decode them.
type FieldSpec = gs1epc.FieldSpec

// GS1FieldSpecs returns the identity fields of a GS1 scheme for one company
// prefix length, so tag generators do not need their own layout tables.
func GS1FieldSpecs(scheme identity.SchemeID, gcpLength int) ([]FieldSpec, error) {
	s, err := gs1epc.Lookup(scheme)
	if err != nil {
		return nil, err
	}
	return s.FieldSpecs(gcpLength)
}

// KeyTypeOf returns the business key type a GS1 scheme carries.
func KeyTypeOf(scheme identity.SchemeID) (identity.KeyType, error) {
	s, err := gs1epc.Lookup(scheme)
	if err != nil {
		return identity.KeyTypeUnknown, err
	}
	return s.KeyType(), nil
}

// SupportedSchemes lists the encodings this build supports.
func SupportedSchemes() []identity.SchemeID {
	out := make([]identity.SchemeID, 0, len(gs1epc.Schemes())+2)
	for _, s := range gs1epc.Schemes() {
		out = append(out, s.ID())
	}
	return append(out, iso.SchemeTransportUnit, iso.Scheme17363)
}

// SourceInfo reports the pinned artefact provenance behind the GS1 metadata,
// so a deployed binary can state which revision of the standard it implements.
func SourceInfo() tdtdef.Source { return tdtdef.Provenance }
