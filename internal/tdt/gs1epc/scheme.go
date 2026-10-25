// Package gs1epc implements GS1 EPC tag encodings for the v2.0.0 baseline:
// SGTIN-96, SSCC-96, GIAI-96 and GRAI-96.
//
// Field layouts, numeric ranges and URI padding come from pinned GS1 TDT
// artefacts (see internal/tdt/tdtdef); the semantics around them — validation,
// promotion to canonical identity, pattern compilation — are reviewed Go here.
// Nothing in this package uses regular expressions or an expression
// interpreter at run time.
//
// Three domains stay separate. A decoded tag yields a canonical
// identity.Value, which carries no filter value, partition or tag length, and
// an Encoding that carries exactly those. Pure Identity URIs render the
// identity; Tag URIs and binary render the encoding. Turning an identity back
// into bits is therefore a parameterised Encode, not a reversal.
//
// Supported schemes are an explicit allowlist. Upstream metadata exists for
// many more; that is not a support commitment, and unsupported schemes return
// tdtcore.ErrUnsupportedScheme.
package gs1epc

import (
	"fmt"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtdef"
)

// supported is the v2.0.0 GS1 baseline.
var supported = map[string]identity.KeyType{
	"SGTIN-96": identity.KeyTypeSGTIN,
	"SSCC-96":  identity.KeyTypeSSCC,
	"GIAI-96":  identity.KeyTypeGIAI,
	"GRAI-96":  identity.KeyTypeGRAI,
}

// componentNames maps TDT field names to canonical component names.
var componentNames = map[string]identity.ComponentName{
	"gs1companyprefix": identity.GS1CompanyPrefix,
	"itemref":          identity.ItemReference,
	"serialref":        identity.SerialReference,
	"serial":           identity.Serial,
	"assettype":        identity.AssetType,
	"indassetref":      identity.IndividualAssetReference,
	"filter":           identity.FilterValue,
}

// Scheme is one supported EPC tag encoding.
type Scheme struct {
	def tdtdef.Scheme
	// id is the tag encoding label, such as "sgtin-96".
	id identity.SchemeID
	// key is the EPC identity key used in Pure Identity and ID Pattern URIs,
	// such as "sgtin".
	key     string
	keyType identity.KeyType
	// partition locates the partition literal, which selects the option.
	partitionOffset int
	partitionBits   int
}

// ID returns the encoding label.
func (s *Scheme) ID() identity.SchemeID { return s.id }

// KeyType returns the business key type the scheme carries.
func (s *Scheme) KeyType() identity.KeyType { return s.keyType }

// TagLength returns the encoding's length in bits.
func (s *Scheme) TagLength() int { return s.def.TagLength }

var (
	byID     = map[identity.SchemeID]*Scheme{}
	byKey    = map[string]*Scheme{}
	byHeader = map[byte]*Scheme{}
	schemes  []*Scheme
)

func init() {
	for _, def := range tdtdef.Schemes {
		keyType, ok := supported[def.Name]
		if !ok {
			continue
		}
		s, err := newScheme(def, keyType)
		if err != nil {
			// Generated metadata that cannot be turned into a layout is a
			// build defect, not a runtime condition.
			panic("gs1epc: " + err.Error())
		}
		schemes = append(schemes, s)
		byID[s.id] = s
		byKey[s.key] = s
		byHeader[byte(def.Header)] = s
	}
	if len(schemes) != len(supported) {
		panic("gs1epc: generated metadata does not cover the supported baseline")
	}
}

func newScheme(def tdtdef.Scheme, keyType identity.KeyType) (*Scheme, error) {
	if def.HeaderBits != 8 {
		return nil, fmt.Errorf("%s: header is %d bits, want 8", def.Name, def.HeaderBits)
	}
	id, ok := strings.CutPrefix(def.TagURIPrefix, "urn:epc:tag:")
	if !ok {
		return nil, fmt.Errorf("%s: unexpected tag URI prefix %q", def.Name, def.TagURIPrefix)
	}
	key, ok := strings.CutPrefix(def.PureURIPrefix, "urn:epc:id:")
	if !ok {
		return nil, fmt.Errorf("%s: unexpected pure identity prefix %q", def.Name, def.PureURIPrefix)
	}
	s := &Scheme{
		def:     def,
		id:      identity.SchemeID(strings.TrimSuffix(id, ":")),
		key:     strings.TrimSuffix(key, ":"),
		keyType: keyType,
	}
	// The partition is only a literal upstream. Locate it once, and require
	// every option to place it identically so option selection is a plain bit
	// read rather than a search.
	for i, opt := range def.Options {
		offset, bits, err := partitionSite(opt)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", def.Name, err)
		}
		if i == 0 {
			s.partitionOffset, s.partitionBits = offset, bits
			continue
		}
		if offset != s.partitionOffset || bits != s.partitionBits {
			return nil, fmt.Errorf("%s: partition moves between options", def.Name)
		}
	}
	for _, opt := range def.Options {
		for _, f := range append(append([]tdtdef.URIField(nil), opt.TagFields...), opt.PureFields...) {
			if !numericCharset(f.CharacterSet) {
				return nil, fmt.Errorf("%s: field %s uses unsupported character set %q", def.Name, f.Name, f.CharacterSet)
			}
		}
	}
	if _, ok := componentOrderOf(s, def.Options[0]); !ok {
		return nil, fmt.Errorf("%s: pure identity fields do not match the canonical component order", def.Name)
	}
	// Every baseline scheme's tag encoding is its pure identity prefixed with
	// the filter value, so decoding renders one field vector and reuses its
	// tail as the identity. Verify that rather than assuming it.
	for _, opt := range def.Options {
		if len(opt.TagFields) != len(opt.PureFields)+1 || opt.TagFields[0].Name != "filter" {
			return nil, fmt.Errorf("%s: tag encoding is not the pure identity plus a filter", def.Name)
		}
		for i, f := range opt.PureFields {
			// Only the rendering is shared. The two levels declare different
			// maxima on purpose — the tag level is bounded by the bits
			// available, the pure identity level by the key syntax — and each
			// level is validated against its own.
			if !sameRendering(opt.TagFields[i+1], f) {
				return nil, fmt.Errorf("%s: tag field %s renders differently from the pure identity field", def.Name, f.Name)
			}
		}
	}
	return s, nil
}

// sameRendering reports whether two fields produce identical text.
func sameRendering(a, b tdtdef.URIField) bool {
	return a.Name == b.Name && a.Length == b.Length && a.Fixed == b.Fixed &&
		a.PadChar == b.PadChar && a.PadDir == b.PadDir
}

func partitionSite(opt tdtdef.Option) (offset, bits int, err error) {
	var gcp tdtdef.BinaryField
	for _, f := range opt.BinaryFields {
		if f.Name == "gs1companyprefix" {
			gcp = f
		}
	}
	if gcp.BitLength == 0 {
		return 0, 0, fmt.Errorf("option %d has no company prefix field", opt.GCPLength)
	}
	for _, l := range opt.BinaryLiterals {
		if l.BitOffset+l.BitLength == gcp.BitOffset {
			return l.BitOffset, l.BitLength, nil
		}
	}
	return 0, 0, fmt.Errorf("option %d has no partition literal", opt.GCPLength)
}

// componentOrderOf checks that the option's pure identity fields are exactly
// the canonical identity component order for the scheme's key type.
func componentOrderOf(s *Scheme, opt tdtdef.Option) ([]identity.ComponentName, bool) {
	order := identity.ComponentOrder(s.keyType)
	if len(order) != len(opt.PureFields) {
		return nil, false
	}
	for i, f := range opt.PureFields {
		if componentNames[f.Name] != order[i] {
			return nil, false
		}
	}
	return order, true
}

// Schemes returns the supported schemes.
func Schemes() []*Scheme { return append([]*Scheme(nil), schemes...) }

// Lookup returns the scheme with the given encoding label.
func Lookup(id identity.SchemeID) (*Scheme, error) {
	s, ok := byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", tdtcore.ErrUnsupportedScheme, id)
	}
	return s, nil
}

// LookupKey returns the scheme for an EPC identity key such as "sgtin".
func LookupKey(key string) (*Scheme, error) {
	s, ok := byKey[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", tdtcore.ErrUnsupportedScheme, key)
	}
	return s, nil
}

// bits reads n bits starting at offset, most significant bit first.
func bitsAt(data []byte, offset, n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		bit := offset + i
		b := data[bit/8] >> (7 - uint(bit%8)) & 1
		v = v<<1 | uint64(b)
	}
	return v
}

// setBits writes n bits of v starting at offset, most significant bit first.
func setBits(data []byte, offset, n int, v uint64) {
	for i := 0; i < n; i++ {
		bit := offset + i
		if v>>(uint(n-1-i))&1 == 1 {
			data[bit/8] |= 1 << (7 - uint(bit%8))
		}
	}
}

// FieldSpec describes one identity field of a scheme option, for callers that
// build identities rather than decode them, such as tag generators.
type FieldSpec struct {
	Name identity.ComponentName
	// Length is the field's fixed digit count, or zero when the field is
	// rendered as a plain decimal.
	Length int
	Fixed  bool
	// Max is the largest value the encoding can carry, which is the binary
	// limit rather than the looser key-syntax limit.
	Max uint64
}

// FieldSpecs returns the identity fields for one company prefix length.
func (s *Scheme) FieldSpecs(gcpLength int) ([]FieldSpec, error) {
	opt, ok := s.def.Option(gcpLength)
	if !ok {
		return nil, tdtcore.Field(string(s.id), "gs1companyprefix", "length", fmt.Sprint(gcpLength),
			"TDS 2.2 company prefix lengths 6-12")
	}
	out := make([]FieldSpec, len(opt.PureFields))
	for i, f := range opt.PureFields {
		max := opt.BinaryFields[i+1].DecimalMax
		if f.DecimalMax < max {
			max = f.DecimalMax
		}
		out[i] = FieldSpec{Name: componentNames[f.Name], Length: f.Length, Fixed: f.Fixed, Max: max}
	}
	return out, nil
}
