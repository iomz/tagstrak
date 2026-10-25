// Package tdtdef holds machine-readable GS1 Tag Data Translation (TDT) scheme
// metadata, generated from pinned upstream artefacts.
//
// The definitions in this package describe *layout* only: where each field sits
// in a tag encoding, how wide it is, how it is padded in URI form, and what
// numeric range the standard allows. Semantics — validation rules, promotion to
// canonical identity, pattern matching — live in reviewed Go in the packages
// above this one, never here.
//
// Provenance: the artefacts are vendored under artefacts/ and pinned by commit
// and SHA-256 in artefacts.lock.json. Regenerate with:
//
//	go generate ./internal/tdt/...
//
// Generation reads the vendored bytes only and never reaches the network.
package tdtdef

//go:generate go run ./cmd/gentdt -root ../../..

// PadDir selects the side a URI field is padded on.
type PadDir uint8

// Padding directions used by TDT URI fields.
const (
	PadNone PadDir = iota
	PadLeft
	PadRight
)

// BinaryField is one variable field inside a tag's bit encoding.
type BinaryField struct {
	Name       string
	BitOffset  int
	BitLength  int
	DecimalMax uint64
}

// BinaryLiteral is a fixed bit sequence inside a tag's bit encoding, such as
// the EPC header, the partition value, or SSCC's reserved trailing zeros.
type BinaryLiteral struct {
	BitOffset int
	BitLength int
	Value     uint64
}

// URIField is one field of a TDT URI level (tag encoding or pure identity).
//
// Fixed reports whether the upstream artefact declares a length. An unfixed
// field is rendered as a plain decimal with no padding, which is how TDS
// forbids leading zeros on serial-style fields. A fixed length of zero is a
// field that must be empty, such as GRAI-96's asset type when the company
// prefix takes all twelve digits.
type URIField struct {
	Name         string
	Length       int
	Fixed        bool
	PadChar      byte
	PadDir       PadDir
	CharacterSet string
	DecimalMax   uint64
}

// Option is one GS1 Company Prefix length variant of a scheme. GCPLength and
// Partition are two views of the same upstream optionKey.
type Option struct {
	GCPLength      int
	Partition      int
	BinaryFields   []BinaryField
	BinaryLiterals []BinaryLiteral
	TagFields      []URIField
	PureFields     []URIField
}

// Scheme is one EPC tag encoding scheme.
type Scheme struct {
	Name          string
	TagLength     int
	Header        uint64
	HeaderBits    int
	TagURIPrefix  string
	PureURIPrefix string
	Options       []Option
}

// Option returns the option for the given GS1 Company Prefix length.
func (s Scheme) Option(gcpLength int) (Option, bool) {
	for _, o := range s.Options {
		if o.GCPLength == gcpLength {
			return o, true
		}
	}
	return Option{}, false
}

// OptionByPartition returns the option selected by a partition value.
func (s Scheme) OptionByPartition(partition int) (Option, bool) {
	for _, o := range s.Options {
		if o.Partition == partition {
			return o, true
		}
	}
	return Option{}, false
}
