package gs1epc

import (
	"strconv"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtdef"
)

// The baseline schemes use only numeric character sets. Anything else would
// need reviewed semantics of its own rather than a generic interpreter, so it
// is rejected at registry build time.
func numericCharset(set string) bool {
	return set == "[0-9]*" || set == "[0-7]*"
}

// render turns a decoded numeric field into its canonical URI text.
//
// A field with a declared length is zero padded to it. A field without one —
// SGTIN and GRAI serials, GIAI asset references — is rendered as a plain
// decimal, which is how TDS forbids leading zeros on those fields.
func render(f tdtdef.URIField, v uint64) string {
	if f.Fixed && f.Length == 0 {
		// A field the standard fixes at zero length, such as GRAI-96's asset
		// type when the company prefix takes all twelve digits.
		return ""
	}
	s := strconv.FormatUint(v, 10)
	if f.Length == 0 || len(s) >= f.Length {
		return s
	}
	pad := f.PadChar
	if pad == 0 {
		pad = '0'
	}
	padding := strings.Repeat(string(pad), f.Length-len(s))
	if f.PadDir == tdtdef.PadRight {
		return s + padding
	}
	return padding + s
}

// parseField validates one URI field's text and returns its numeric value.
//
// Validation is deliberately strict in both directions: a padded field must
// carry exactly its declared length, and an unpadded field must not carry
// leading zeros, so that parse and render round trip exactly.
func parseField(scheme string, f tdtdef.URIField, text string) (uint64, error) {
	if f.Fixed && f.Length == 0 {
		if text != "" {
			return 0, tdtcore.Field(scheme, f.Name, "length", text, "TDS 2.2 zero-length field")
		}
		return 0, nil
	}
	if text == "" {
		return 0, tdtcore.Field(scheme, f.Name, "empty", text, "")
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, tdtcore.Field(scheme, f.Name, "charset", text, "TDS 2.2 "+f.CharacterSet)
		}
	}
	switch {
	case f.Fixed && len(text) != f.Length:
		return 0, tdtcore.Field(scheme, f.Name, "length", text, "TDS 2.2 padded field")
	case !f.Fixed && len(text) > 1 && text[0] == '0':
		return 0, tdtcore.Field(scheme, f.Name, "leading-zero", text, "TDS 2.2 unpadded field")
	}
	v, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return 0, tdtcore.Field(scheme, f.Name, "range", text, "")
	}
	if v > f.DecimalMax {
		return 0, tdtcore.Field(scheme, f.Name, "range", text, "TDS 2.2 maximum "+strconv.FormatUint(f.DecimalMax, 10))
	}
	return v, nil
}

// components renders a field vector from decoded numeric values.
func components(fields []tdtdef.URIField, values []uint64) []identity.Component {
	out := make([]identity.Component, len(fields))
	for i, f := range fields {
		out[i] = identity.Component{Name: componentNames[f.Name], Value: render(f, values[i])}
	}
	return out
}
