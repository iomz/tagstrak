package gs1epc

import (
	"fmt"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

// EncodeParams carries the encoding-control fields that a canonical identity
// deliberately does not have.
//
// Encoding an identity is therefore a parameterised operation, not the inverse
// of decoding: the same identity can be written with any filter value.
type EncodeParams struct {
	// Scheme selects the tag encoding, such as "sgtin-96".
	Scheme identity.SchemeID
	// FilterValue is the EPC filter to write.
	FilterValue uint8
}

// Encode writes a canonical identity into the requested tag encoding.
//
// The scheme is never chosen or substituted on the caller's behalf: an
// identity that does not fit the requested encoding returns an error rather
// than being promoted to a longer one.
func Encode(v identity.Value, params EncodeParams) (tdtcore.Translation, error) {
	var out tdtcore.Translation
	s, err := Lookup(params.Scheme)
	if err != nil {
		return out, err
	}
	if v.Authority() != identity.AuthorityGS1 || v.KeyType() != s.keyType {
		return out, fmt.Errorf("%w: %s cannot carry %s", tdtcore.ErrCannotEncode, s.id, v.KeyType())
	}
	opt, err := s.optionForValue(v)
	if err != nil {
		return out, err
	}
	if v.Len() != len(opt.PureFields) {
		return out, fmt.Errorf("%w: %s needs %d components, identity has %d",
			tdtcore.ErrCannotEncode, s.id, len(opt.PureFields), v.Len())
	}

	values := make([]uint64, 0, len(opt.BinaryFields))
	values = append(values, uint64(params.FilterValue))
	if uint64(params.FilterValue) > opt.TagFields[0].DecimalMax {
		return out, tdtcore.Field(string(s.id), "filter", "range", fmt.Sprint(params.FilterValue),
			"TDS 2.2 filter value maximum")
	}
	for i, f := range opt.PureFields {
		c := v.At(i)
		if c.Name != componentNames[f.Name] {
			return out, fmt.Errorf("%w: component %d is %s, want %s", tdtcore.ErrCannotEncode, i, c.Name, f.Name)
		}
		n, err := parseField(string(s.id), f, c.Value)
		if err != nil {
			return out, err
		}
		if render(f, n) != c.Value {
			return out, tdtcore.Field(string(s.id), f.Name, "not-canonical", c.Value, "TDS 2.2 field padding")
		}
		values = append(values, n)
	}

	bits := make([]byte, (s.def.TagLength+7)/8)
	for _, l := range opt.BinaryLiterals {
		setBits(bits, l.BitOffset, l.BitLength, l.Value)
	}
	for i, f := range opt.BinaryFields {
		if values[i] > f.DecimalMax {
			return out, tdtcore.Field(string(s.id), f.Name, "range", fmt.Sprint(values[i]),
				"TDS 2.2 binary field maximum")
		}
		setBits(bits, f.BitOffset, f.BitLength, values[i])
	}

	control := tdtcore.Control{
		FilterValue:  params.FilterValue,
		HasFilter:    true,
		Partition:    uint8(opt.Partition),
		HasPartition: true,
		GCPLength:    opt.GCPLength,
	}
	out.Identity = v
	out.Encoding = tdtcore.NewEncoding(s.id, s.def.TagLength, s.def.TagLength, control, bits,
		components(opt.TagFields, values))
	return out, nil
}
