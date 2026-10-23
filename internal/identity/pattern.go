package identity

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SchemeID is an opaque encoding label owned by the standard-specific package
// that defines it, such as "sgtin-96" or "iso17363". The identity layer only
// compares it; it never interprets it.
type SchemeID string

// PatternLevel selects the domain a pattern constrains.
type PatternLevel uint8

// Pattern levels.
const (
	// LevelIdentity constrains the canonical identity only. Such a pattern is
	// independent of filter value, partition and tag length by construction.
	LevelIdentity PatternLevel = iota + 1
	// LevelEncoding constrains a specific tag encoding, including its
	// encoding-control fields.
	LevelEncoding
)

// String returns the level name.
func (l PatternLevel) String() string {
	switch l {
	case LevelIdentity:
		return "identity"
	case LevelEncoding:
		return "encoding"
	default:
		return "unknown"
	}
}

// FieldKind selects how one pattern field constrains its component.
type FieldKind uint8

// Field kinds.
const (
	FieldExact FieldKind = iota + 1
	FieldAny
	FieldRange
)

// FieldPattern constrains one component.
type FieldPattern struct {
	Name ComponentName
	Kind FieldKind
	// Text is the required value when Kind is FieldExact.
	Text string
	// Lo and Hi are the inclusive bounds when Kind is FieldRange.
	Lo, Hi uint64
}

// Errors returned when compiling or constructing patterns.
var (
	ErrPatternLevel  = errors.New("identity: invalid pattern level")
	ErrPatternFields = errors.New("identity: invalid pattern fields")
	ErrPatternStar   = errors.New("identity: a star field must be followed only by star fields")
)

// BitPattern is a fixed-position value/mask test over a tag's bits.
//
// A mask bit set to 1 means the corresponding value bit must match. It is
// supplied only when testing it is semantically equivalent to matching the
// pattern's fields, so a consumer may always use it when present and never has
// to know how a scheme lays its fields out.
type BitPattern struct {
	value  []byte
	mask   []byte
	bitLen int
}

// NewBitPattern builds a bit pattern. The value and mask must be the same
// length and cover at least bitLen bits.
func NewBitPattern(value, mask []byte, bitLen int) (*BitPattern, error) {
	if len(value) != len(mask) || bitLen < 0 || (bitLen+7)/8 > len(value) {
		return nil, fmt.Errorf("%w: malformed bit pattern", ErrPatternFields)
	}
	return &BitPattern{
		value:  append([]byte(nil), value...),
		mask:   append([]byte(nil), mask...),
		bitLen: bitLen,
	}, nil
}

// BitLen returns the number of bits the pattern covers.
func (b *BitPattern) BitLen() int { return b.bitLen }

// Value returns a copy of the pattern's value bytes.
func (b *BitPattern) Value() []byte { return append([]byte(nil), b.value...) }

// Mask returns a copy of the pattern's mask bytes.
func (b *BitPattern) Mask() []byte { return append([]byte(nil), b.mask...) }

// Match reports whether the tag bits satisfy the pattern.
func (b *BitPattern) Match(bits []byte, bitLen int) bool {
	if bitLen < b.bitLen || len(bits) < len(b.mask) {
		return false
	}
	for i, m := range b.mask {
		if m == 0 {
			continue
		}
		if bits[i]&m != b.value[i]&m {
			return false
		}
	}
	return true
}

// Pattern is an immutable compiled matcher input.
//
// Patterns are produced by the translation layer, never by consumers parsing
// identifier syntax themselves. A Pattern is safe for concurrent use and holds
// no references into caller memory.
type Pattern struct {
	level     PatternLevel
	authority Authority
	keyType   KeyType
	scheme    SchemeID
	fields    []FieldPattern
	bits      *BitPattern
	text      string
}

// NewPattern builds a pattern from already-validated parts.
//
// Fields must be in the canonical order for the level: the identity component
// order for LevelIdentity, and the scheme's encoding order for LevelEncoding.
// Star fields must form a suffix, which is what makes prefix matching well
// defined.
func NewPattern(level PatternLevel, authority Authority, keyType KeyType, scheme SchemeID, fields []FieldPattern, bits *BitPattern, text string) (Pattern, error) {
	switch level {
	case LevelIdentity:
		if scheme != "" {
			return Pattern{}, fmt.Errorf("%w: identity-level patterns carry no scheme", ErrPatternLevel)
		}
		if bits != nil {
			return Pattern{}, fmt.Errorf("%w: identity-level patterns carry no bit pattern", ErrPatternLevel)
		}
	case LevelEncoding:
		if scheme == "" {
			return Pattern{}, fmt.Errorf("%w: encoding-level patterns require a scheme", ErrPatternLevel)
		}
	default:
		return Pattern{}, fmt.Errorf("%w: %d", ErrPatternLevel, level)
	}
	if len(fields) == 0 {
		return Pattern{}, fmt.Errorf("%w: no fields", ErrPatternFields)
	}
	seenAny := false
	cloned := make([]FieldPattern, len(fields))
	for i, f := range fields {
		switch f.Kind {
		case FieldAny:
			seenAny = true
		case FieldExact, FieldRange:
			if seenAny {
				return Pattern{}, ErrPatternStar
			}
			if f.Kind == FieldRange && f.Lo > f.Hi {
				return Pattern{}, fmt.Errorf("%w: range %d-%d", ErrPatternFields, f.Lo, f.Hi)
			}
		default:
			return Pattern{}, fmt.Errorf("%w: field %s has no kind", ErrPatternFields, f.Name)
		}
		cloned[i] = f
	}
	if level == LevelIdentity {
		order := componentOrder[keyType]
		if len(order) != len(cloned) {
			return Pattern{}, fmt.Errorf("%w: %d fields for %s, want %d", ErrPatternFields, len(cloned), keyType, len(order))
		}
		for i, f := range cloned {
			if f.Name != order[i] {
				return Pattern{}, fmt.Errorf("%w: position %d is %s, want %s", ErrPatternFields, i, f.Name, order[i])
			}
		}
	}
	return Pattern{
		level: level, authority: authority, keyType: keyType,
		scheme: scheme, fields: cloned, bits: bits, text: text,
	}, nil
}

// Level returns the domain the pattern constrains.
func (p Pattern) Level() PatternLevel { return p.level }

// Authority returns the numbering authority the pattern applies to.
func (p Pattern) Authority() Authority { return p.authority }

// KeyType returns the key type the pattern applies to.
func (p Pattern) KeyType() KeyType { return p.keyType }

// Scheme returns the encoding the pattern applies to, empty at identity level.
func (p Pattern) Scheme() SchemeID { return p.scheme }

// Len returns the number of pattern fields.
func (p Pattern) Len() int { return len(p.fields) }

// Field returns the pattern field at index i.
func (p Pattern) Field(i int) FieldPattern { return p.fields[i] }

// Fields returns a copy of the pattern fields.
func (p Pattern) Fields() []FieldPattern { return append([]FieldPattern(nil), p.fields...) }

// Bits returns the pattern's bit test, or nil when field matching is required.
func (p Pattern) Bits() *BitPattern { return p.bits }

// IsZero reports whether the pattern is unset.
func (p Pattern) IsZero() bool { return p.level == 0 }

// String returns the source text the pattern was compiled from, where one
// exists. Programmatically built patterns render through Describe instead.
func (p Pattern) String() string {
	if p.text != "" {
		return p.text
	}
	return p.Describe()
}

// Describe renders a diagnostic form of the pattern.
func (p Pattern) Describe() string {
	var b strings.Builder
	b.WriteString(p.level.String())
	b.WriteByte(':')
	if p.scheme != "" {
		b.WriteString(string(p.scheme))
	} else {
		b.WriteString(p.keyType.String())
	}
	b.WriteByte('[')
	for i, f := range p.fields {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(f.Name.String())
		b.WriteByte('=')
		switch f.Kind {
		case FieldAny:
			b.WriteByte('*')
		case FieldRange:
			b.WriteByte('[')
			b.WriteString(strconv.FormatUint(f.Lo, 10))
			b.WriteByte('-')
			b.WriteString(strconv.FormatUint(f.Hi, 10))
			b.WriteByte(']')
		default:
			b.WriteString(f.Text)
		}
	}
	b.WriteByte(']')
	return b.String()
}

// MatchValue reports whether an identity satisfies an identity-level pattern.
// Encoding-level patterns never match a bare identity, because they constrain
// fields an identity does not carry.
func (p Pattern) MatchValue(v Value) bool {
	if p.level != LevelIdentity || p.authority != v.authority || p.keyType != v.keyType {
		return false
	}
	return p.matchComponents(v.components)
}

// MatchComponents reports whether a component vector satisfies the pattern.
// The vector must be in the same canonical order the pattern was built with;
// the translation layer produces both.
//
// This is the reference implementation of match semantics. Optimised matchers
// must agree with it on the published conformance corpus.
func (p Pattern) MatchComponents(components []Component) bool {
	return p.matchComponents(components)
}

func (p Pattern) matchComponents(components []Component) bool {
	if len(components) != len(p.fields) {
		return false
	}
	for i, f := range p.fields {
		c := components[i]
		if c.Name != f.Name {
			return false
		}
		switch f.Kind {
		case FieldAny:
			continue
		case FieldExact:
			if c.Value != f.Text {
				return false
			}
		case FieldRange:
			n, err := strconv.ParseUint(c.Value, 10, 64)
			if err != nil || n < f.Lo || n > f.Hi {
				return false
			}
		default:
			return false
		}
	}
	return true
}
