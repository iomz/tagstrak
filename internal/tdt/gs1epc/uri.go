package gs1epc

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

// URI prefixes defined by TDS for the levels this package implements.
const (
	pureIdentityPrefix = "urn:epc:id:"
	tagEncodingPrefix  = "urn:epc:tag:"
	idPatternPrefix    = "urn:epc:idpat:"
	tagPatternPrefix   = "urn:epc:pat:"
)

// schemeForKeyType returns the single baseline encoding for a key type.
func schemeForKeyType(k identity.KeyType) (*Scheme, error) {
	for _, s := range schemes {
		if s.keyType == k {
			return s, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", tdtcore.ErrUnsupportedScheme, k)
}

// PureIdentityURI renders an EPC Pure Identity URI for a canonical identity.
//
// This is the standards-backed textual form of a GS1 identity, which is why it
// lives here and not on identity.Value. It carries no filter value or tag
// length, so it is a lossy view of any particular tag.
func PureIdentityURI(v identity.Value) (string, error) {
	s, err := schemeForKeyType(v.KeyType())
	if err != nil {
		return "", err
	}
	opt, err := s.optionForValue(v)
	if err != nil {
		return "", err
	}
	if v.Len() != len(opt.PureFields) {
		return "", fmt.Errorf("%w: %s needs %d components", tdtcore.ErrCannotEncode, s.id, len(opt.PureFields))
	}
	var b strings.Builder
	b.WriteString(s.def.PureURIPrefix)
	for i, f := range opt.PureFields {
		c := v.At(i)
		n, err := parseField(string(s.id), f, c.Value)
		if err != nil {
			return "", err
		}
		if render(f, n) != c.Value {
			return "", tdtcore.Field(string(s.id), f.Name, "not-canonical", c.Value, "TDS 2.2 field padding")
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(c.Value)
	}
	return b.String(), nil
}

// TagURI renders an EPC Tag URI, which includes the encoding-control fields.
func TagURI(e tdtcore.Encoding) (string, error) {
	s, err := Lookup(e.Scheme)
	if err != nil {
		return "", err
	}
	fields := e.RawFields()
	if len(fields) == 0 {
		return "", fmt.Errorf("%w: encoding carries no fields", tdtcore.ErrCannotEncode)
	}
	var b strings.Builder
	b.WriteString(s.def.TagURIPrefix)
	for i, c := range fields {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(c.Value)
	}
	return b.String(), nil
}

// ParsePureIdentityURI parses an EPC Pure Identity URI into a canonical
// identity. The result carries no encoding, because a Pure Identity URI does
// not say how the identity was, or will be, written onto a tag.
func ParsePureIdentityURI(uri string) (identity.Value, error) {
	rest, ok := strings.CutPrefix(uri, pureIdentityPrefix)
	if !ok {
		return identity.Value{}, fmt.Errorf("%w: %q is not a pure identity URI", tdtcore.ErrMalformedURI, uri)
	}
	key, body, ok := strings.Cut(rest, ":")
	if !ok {
		return identity.Value{}, fmt.Errorf("%w: %q", tdtcore.ErrMalformedURI, uri)
	}
	s, err := LookupKey(key)
	if err != nil {
		return identity.Value{}, err
	}
	parts := strings.Split(body, ".")
	opt, ok := s.def.Option(len(parts[0]))
	if !ok {
		return identity.Value{}, tdtcore.Field(string(s.id), "gs1companyprefix", "length", parts[0],
			"TDS 2.2 company prefix lengths 6-12")
	}
	if len(parts) != len(opt.PureFields) {
		return identity.Value{}, fmt.Errorf("%w: %s takes %d fields, got %d",
			tdtcore.ErrMalformedURI, s.id, len(opt.PureFields), len(parts))
	}
	comps := make([]identity.Component, len(parts))
	for i, f := range opt.PureFields {
		if _, err := parseField(string(s.id), f, parts[i]); err != nil {
			return identity.Value{}, err
		}
		comps[i] = identity.Component{Name: componentNames[f.Name], Value: parts[i]}
	}
	return identity.New(identity.AuthorityGS1, s.keyType, comps)
}

// ParseTagURI parses an EPC Tag URI and re-encodes it, so the result carries
// both the identity and the exact bits the URI denotes.
func ParseTagURI(uri string) (tdtcore.Translation, error) {
	var out tdtcore.Translation
	rest, ok := strings.CutPrefix(uri, tagEncodingPrefix)
	if !ok {
		return out, fmt.Errorf("%w: %q is not a tag URI", tdtcore.ErrMalformedURI, uri)
	}
	id, body, ok := strings.Cut(rest, ":")
	if !ok {
		return out, fmt.Errorf("%w: %q", tdtcore.ErrMalformedURI, uri)
	}
	s, err := Lookup(identity.SchemeID(id))
	if err != nil {
		return out, err
	}
	parts := strings.Split(body, ".")
	if len(parts) < 2 {
		return out, fmt.Errorf("%w: %q", tdtcore.ErrMalformedURI, uri)
	}
	opt, ok := s.def.Option(len(parts[1]))
	if !ok {
		return out, tdtcore.Field(string(s.id), "gs1companyprefix", "length", parts[1],
			"TDS 2.2 company prefix lengths 6-12")
	}
	if len(parts) != len(opt.TagFields) {
		return out, fmt.Errorf("%w: %s takes %d fields, got %d",
			tdtcore.ErrMalformedURI, s.id, len(opt.TagFields), len(parts))
	}
	filter, err := parseField(string(s.id), opt.TagFields[0], parts[0])
	if err != nil {
		return out, err
	}
	comps := make([]identity.Component, len(opt.PureFields))
	for i, f := range opt.PureFields {
		text := parts[i+1]
		if _, err := parseField(string(s.id), f, text); err != nil {
			return out, err
		}
		comps[i] = identity.Component{Name: componentNames[f.Name], Value: text}
	}
	value, err := identity.New(identity.AuthorityGS1, s.keyType, comps)
	if err != nil {
		return out, err
	}
	return Encode(value, EncodeParams{Scheme: s.id, FilterValue: uint8(filter)})
}

// FilterValueOf parses a filter value from its URI text.
func FilterValueOf(text string) (uint8, error) {
	v, err := strconv.ParseUint(text, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("%w: filter %q", tdtcore.ErrMalformedURI, text)
	}
	return uint8(v), nil
}
