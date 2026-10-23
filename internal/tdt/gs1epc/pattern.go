package gs1epc

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtdef"
)

// CompilePattern compiles a GS1 pattern URI into an immutable matcher input.
//
// Two families exist at two different levels, and the distinction is load
// bearing:
//
//	urn:epc:idpat:sgtin:0614141.812345.*      identity level, no filter value
//	urn:epc:pat:sgtin-96:3.0614141.812345.*   encoding level, filter included
//
// An ID pattern therefore matches a tag whatever filter value, partition or
// tag length it was written with, because it cannot reach those fields at all.
// Consumers receive compiled patterns and never parse this syntax themselves.
func CompilePattern(text string) (identity.Pattern, error) {
	switch {
	case strings.HasPrefix(text, idPatternPrefix):
		return compileIDPattern(text)
	case strings.HasPrefix(text, tagPatternPrefix):
		return compileTagPattern(text)
	default:
		return identity.Pattern{}, fmt.Errorf("%w: %q is not a GS1 pattern URI", tdtcore.ErrMalformedPattern, text)
	}
}

func compileIDPattern(text string) (identity.Pattern, error) {
	key, parts, err := splitPattern(text, idPatternPrefix)
	if err != nil {
		return identity.Pattern{}, err
	}
	s, err := LookupKey(key)
	if err != nil {
		return identity.Pattern{}, err
	}
	opt, ok := optionForPattern(s, parts, 0)
	if !ok && !allAny(parts) {
		return identity.Pattern{}, tdtcore.Field(string(s.id), "gs1companyprefix", "length", parts[0],
			"TDS 2.2 company prefix lengths 6-12")
	}
	uriFields := s.def.Options[0].PureFields
	if ok {
		uriFields = opt.PureFields
	}
	if len(parts) != len(uriFields) {
		return identity.Pattern{}, fmt.Errorf("%w: %s takes %d fields, got %d",
			tdtcore.ErrMalformedPattern, s.key, len(uriFields), len(parts))
	}
	fields, err := patternFields(s, uriFields, parts, false)
	if err != nil {
		return identity.Pattern{}, err
	}
	// Identity-level patterns never carry a scheme or a bit test: they must
	// stay independent of how the identity was encoded.
	return identity.NewPattern(identity.LevelIdentity, identity.AuthorityGS1, s.keyType, "", fields, nil, text)
}

func compileTagPattern(text string) (identity.Pattern, error) {
	id, parts, err := splitPattern(text, tagPatternPrefix)
	if err != nil {
		return identity.Pattern{}, err
	}
	s, err := Lookup(identity.SchemeID(id))
	if err != nil {
		return identity.Pattern{}, err
	}
	opt, known := optionForPattern(s, parts, 1)
	uriFields := s.def.Options[0].TagFields
	if known {
		uriFields = opt.TagFields
	}
	if len(parts) != len(uriFields) {
		return identity.Pattern{}, fmt.Errorf("%w: %s takes %d fields, got %d",
			tdtcore.ErrMalformedPattern, s.id, len(uriFields), len(parts))
	}
	if !known && !allAnyFrom(parts, 1) {
		return identity.Pattern{}, tdtcore.Field(string(s.id), "gs1companyprefix", "length", parts[1],
			"TDS 2.2 company prefix lengths 6-12")
	}
	fields, err := patternFields(s, uriFields, parts, true)
	if err != nil {
		return identity.Pattern{}, err
	}
	bits, err := patternBits(s, opt, known, fields)
	if err != nil {
		return identity.Pattern{}, err
	}
	return identity.NewPattern(identity.LevelEncoding, identity.AuthorityGS1, s.keyType, s.id, fields, bits, text)
}

func splitPattern(text, prefix string) (string, []string, error) {
	rest := strings.TrimPrefix(text, prefix)
	name, body, ok := strings.Cut(rest, ":")
	if !ok || name == "" || body == "" {
		return "", nil, fmt.Errorf("%w: %q", tdtcore.ErrMalformedPattern, text)
	}
	return name, strings.Split(body, "."), nil
}

// optionForPattern selects the option implied by an exact company prefix.
func optionForPattern(s *Scheme, parts []string, gcpIndex int) (tdtdef.Option, bool) {
	if gcpIndex >= len(parts) {
		return tdtdef.Option{}, false
	}
	gcp := parts[gcpIndex]
	if isAny(gcp) || isRange(gcp) {
		return tdtdef.Option{}, false
	}
	return s.def.Option(len(gcp))
}

func isAny(s string) bool { return s == "*" }

func isRange(s string) bool { return strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") }

func allAny(parts []string) bool { return allAnyFrom(parts, 0) }

func allAnyFrom(parts []string, from int) bool {
	for _, p := range parts[from:] {
		if !isAny(p) {
			return false
		}
	}
	return true
}

// patternFields validates each pattern field against the level's metadata.
// Ranges are accepted only at the encoding level, which is where TDS defines
// them; an ID pattern field is either an exact value or a star.
func patternFields(s *Scheme, uriFields []tdtdef.URIField, parts []string, allowRange bool) ([]identity.FieldPattern, error) {
	out := make([]identity.FieldPattern, len(uriFields))
	for i, f := range uriFields {
		text := parts[i]
		name := componentNames[f.Name]
		switch {
		case isAny(text):
			out[i] = identity.FieldPattern{Name: name, Kind: identity.FieldAny}
		case isRange(text):
			if !allowRange {
				return nil, tdtcore.Field(string(s.id), f.Name, "range-not-allowed", text,
					"TDS 2.2 ID patterns take values or *")
			}
			lo, hi, err := parseRange(s, f, text)
			if err != nil {
				return nil, err
			}
			out[i] = identity.FieldPattern{Name: name, Kind: identity.FieldRange, Lo: lo, Hi: hi}
		default:
			if _, err := parseField(string(s.id), f, text); err != nil {
				return nil, err
			}
			out[i] = identity.FieldPattern{Name: name, Kind: identity.FieldExact, Text: text}
		}
	}
	return out, nil
}

func parseRange(s *Scheme, f tdtdef.URIField, text string) (uint64, uint64, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(text, "["), "]")
	loText, hiText, ok := strings.Cut(body, "-")
	if !ok {
		return 0, 0, tdtcore.Field(string(s.id), f.Name, "range-syntax", text, "TDS 2.2 pattern range")
	}
	lo, err := strconv.ParseUint(loText, 10, 64)
	if err != nil {
		return 0, 0, tdtcore.Field(string(s.id), f.Name, "range-syntax", text, "TDS 2.2 pattern range")
	}
	hi, err := strconv.ParseUint(hiText, 10, 64)
	if err != nil {
		return 0, 0, tdtcore.Field(string(s.id), f.Name, "range-syntax", text, "TDS 2.2 pattern range")
	}
	if lo > hi {
		return 0, 0, tdtcore.Field(string(s.id), f.Name, "range-order", text, "TDS 2.2 pattern range")
	}
	if hi > f.DecimalMax {
		return 0, 0, tdtcore.Field(string(s.id), f.Name, "range", text, "TDS 2.2 maximum "+
			strconv.FormatUint(f.DecimalMax, 10))
	}
	return lo, hi, nil
}

// patternBits derives a value/mask bit test, but only when testing it is
// exactly equivalent to matching the pattern's fields.
//
// That holds when every constrained field occupies a known fixed bit range,
// which needs the option, which needs an exact company prefix. A range never
// maps onto a mask, and a pattern whose option is unknown cannot pin anything
// but the header, so those cases return nil and the consumer matches fields
// instead. Identity-level patterns never reach this function at all.
func patternBits(s *Scheme, opt tdtdef.Option, known bool, fields []identity.FieldPattern) (*identity.BitPattern, error) {
	size := (s.def.TagLength + 7) / 8
	value := make([]byte, size)
	mask := make([]byte, size)

	pin := func(offset, length int, v uint64) {
		setBits(value, offset, length, v)
		setBits(mask, offset, length, ^uint64(0)>>(64-uint(length)))
	}
	pin(0, s.def.HeaderBits, s.def.Header)

	constrained := false
	for _, f := range fields {
		switch f.Kind {
		case identity.FieldRange:
			return nil, nil
		case identity.FieldExact:
			constrained = true
		}
	}
	if !known {
		if constrained {
			// Something is constrained that cannot be located without the
			// option, so a header-only test would match too much.
			return nil, nil
		}
		// Every field is a star: the header alone is exactly this pattern.
		headerBytes := (s.def.HeaderBits + 7) / 8
		return identity.NewBitPattern(value[:headerBytes], mask[:headerBytes], s.def.HeaderBits)
	}
	covered := s.def.TagLength
	for i, f := range fields {
		bf := opt.BinaryFields[i]
		if f.Kind == identity.FieldAny {
			covered = min(covered, bf.BitOffset)
			continue
		}
		var n uint64
		if f.Text != "" {
			// An empty exact field is a zero-length field, such as GRAI-96's
			// asset type with a twelve digit company prefix.
			parsed, err := strconv.ParseUint(f.Text, 10, 64)
			if err != nil {
				return nil, tdtcore.Field(string(s.id), bf.Name, "range", f.Text, "")
			}
			n = parsed
		}
		pin(bf.BitOffset, bf.BitLength, n)
	}
	// Clear anything at or beyond the first unconstrained field so the mask
	// never claims bits the pattern does not constrain.
	for bit := covered; bit < s.def.TagLength; bit++ {
		mask[bit/8] &^= 1 << (7 - uint(bit%8))
		value[bit/8] &^= 1 << (7 - uint(bit%8))
	}
	// Literals hold on every tag of this option, wherever they sit, so pin
	// them again: SSCC-96's reserved bits follow the serial reference and the
	// clearing loop above would otherwise drop them.
	for _, l := range opt.BinaryLiterals {
		pin(l.BitOffset, l.BitLength, l.Value)
	}
	return identity.NewBitPattern(value, mask, s.def.TagLength)
}
