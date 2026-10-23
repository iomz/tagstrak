package gs1epc

import (
	"fmt"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtdef"
)

// Decode translates EPC tag bits into a canonical identity and its encoding.
//
// The stages are separate on purpose: recognise the scheme from the header,
// decode the bit layout, validate every field against the standard's declared
// ranges, then promote the result into an identity plus encoding-control
// fields. Each stage returns its own error class.
func Decode(bits []byte, bitLen int) (tdtcore.Translation, error) {
	var out tdtcore.Translation
	if bitLen < 8 || len(bits)*8 < bitLen {
		return out, fmt.Errorf("%w: %d bits", tdtcore.ErrTruncated, bitLen)
	}
	s, ok := byHeader[bits[0]]
	if !ok {
		return out, fmt.Errorf("%w: header 0x%02x", tdtcore.ErrUnknownHeader, bits[0])
	}
	return s.Decode(bits, bitLen)
}

// Decode translates tag bits known to belong to this scheme.
func (s *Scheme) Decode(bits []byte, bitLen int) (tdtcore.Translation, error) {
	var out tdtcore.Translation
	if bitLen < s.def.TagLength || len(bits)*8 < s.def.TagLength {
		return out, fmt.Errorf("%w: %s needs %d bits, got %d", tdtcore.ErrTruncated, s.id, s.def.TagLength, bitLen)
	}
	partition := int(bitsAt(bits, s.partitionOffset, s.partitionBits))
	opt, ok := s.def.OptionByPartition(partition)
	if !ok {
		return out, fmt.Errorf("%w: %s partition %d", tdtcore.ErrUnknownPartition, s.id, partition)
	}
	for _, l := range opt.BinaryLiterals {
		if bitsAt(bits, l.BitOffset, l.BitLength) != l.Value {
			return out, fmt.Errorf("%w: %s at bit %d", tdtcore.ErrReservedBits, s.id, l.BitOffset)
		}
	}

	values := make([]uint64, len(opt.BinaryFields))
	var filter uint64
	for i, f := range opt.BinaryFields {
		v := bitsAt(bits, f.BitOffset, f.BitLength)
		if v > f.DecimalMax {
			return out, tdtcore.Field(string(s.id), f.Name, "range", fmt.Sprint(v), "TDS 2.2 binary field maximum")
		}
		values[i] = v
		if f.Name == "filter" {
			filter = v
		}
	}

	// The tag encoding is the pure identity prefixed with the filter value
	// (checked when the registry is built), so one rendering pass serves both
	// the encoding vector and the identity.
	tagFields := components(opt.TagFields, values)
	value, err := identity.New(identity.AuthorityGS1, s.keyType, tagFields[1:])
	if err != nil {
		return out, err
	}
	control := tdtcore.Control{
		FilterValue:  uint8(filter),
		HasFilter:    true,
		Partition:    uint8(partition),
		HasPartition: true,
		GCPLength:    opt.GCPLength,
	}
	encoded := make([]byte, (s.def.TagLength+7)/8)
	copy(encoded, bits[:len(encoded)])
	out.Identity = value
	out.Encoding = tdtcore.NewEncoding(s.id, s.def.TagLength, s.def.TagLength, control, encoded, tagFields)
	return out, nil
}

// optionForValue selects the option a canonical identity belongs to, using the
// company prefix digit count the identity already carries.
func (s *Scheme) optionForValue(v identity.Value) (tdtdef.Option, error) {
	gcp, ok := v.Component(identity.GS1CompanyPrefix)
	if !ok {
		return tdtdef.Option{}, tdtcore.Field(string(s.id), "gs1companyprefix", "missing", "", "")
	}
	opt, ok := s.def.Option(len(gcp.Value))
	if !ok {
		return tdtdef.Option{}, tdtcore.Field(string(s.id), "gs1companyprefix", "length", gcp.Value,
			"TDS 2.2 company prefix lengths 6-12")
	}
	return opt, nil
}
