// Package identity holds canonical, protocol-neutral identities and the
// compiled pattern values that matching consumes.
//
// A Value answers "what is this thing", never "how was it written onto a tag".
// Encoding-control fields — EPC filter value, partition, tag length, Gen2 PC
// toggle, AFI, residue bits — are properties of a representation and live with
// the encoding, not here. Two tags that carry the same trade item instance
// under different filter values produce equal Values.
//
// Canonicality here is structural, not textual. There is deliberately no
// universal string form: a Value has no String method, equality runs over the
// typed components, and Key provides an opaque comparable map key. Textual
// forms belong to the standard that defines them — EPC Pure Identity URIs in
// internal/tdt/gs1epc, for example — so that a diagnostic rendering can never
// quietly become an interchange format.
package identity

import (
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
)

// Authority names the body whose numbering system defines an identity.
type Authority uint8

// Supported authorities.
//
// An authority is the body whose numbering system defines an identity, not the
// application standard that happens to carry it. ISO/IEC 17360 is an
// application standard and assigns no identifiers, so it is deliberately not
// an authority here; the numbering authority behind its transport unit
// licence plates is ISO/IEC 15459 (issue #8 Amendment 2, decision B5).
const (
	AuthorityUnknown Authority = iota
	AuthorityGS1
	AuthorityISO6346
	AuthorityISO15459
)

// String returns the authority name. It is diagnostic output, not a contract.
func (a Authority) String() string {
	switch a {
	case AuthorityGS1:
		return "gs1"
	case AuthorityISO6346:
		return "iso6346"
	case AuthorityISO15459:
		return "iso15459"
	default:
		return "unknown"
	}
}

// KeyType names the kind of business key an identity carries.
type KeyType uint8

// Supported key types.
const (
	KeyTypeUnknown KeyType = iota
	KeyTypeSGTIN
	KeyTypeSSCC
	KeyTypeGRAI
	KeyTypeGIAI
	KeyTypeFreightContainer
	KeyTypeTransportUnit
)

// String returns the key type name. It is diagnostic output, not a contract.
func (k KeyType) String() string {
	switch k {
	case KeyTypeSGTIN:
		return "sgtin"
	case KeyTypeSSCC:
		return "sscc"
	case KeyTypeGRAI:
		return "grai"
	case KeyTypeGIAI:
		return "giai"
	case KeyTypeFreightContainer:
		return "freight-container"
	case KeyTypeTransportUnit:
		return "transport-unit"
	default:
		return "unknown"
	}
}

// ComponentName names one field of an identity or an encoding.
type ComponentName uint8

// Component names. Identity-level names appear in ComponentOrder; the
// remaining names are encoding-control fields that only ever appear in
// encoding-level component vectors.
const (
	ComponentUnknown ComponentName = iota
	// GS1 identity components.
	GS1CompanyPrefix
	ItemReference
	SerialReference
	Serial
	AssetType
	IndividualAssetReference
	// ISO identity components.
	DataIdentifier
	OwnerCode
	EquipmentCategory
	ContainerSerial
	CheckDigit
	ISOIdentifier
	// Encoding-control components. Never part of an identity.
	FilterValue
	Partition
)

// String returns the component name used in TDT grammars where one exists.
// It is diagnostic and pattern-facing output, not an identity serialization.
func (c ComponentName) String() string {
	switch c {
	case GS1CompanyPrefix:
		return "gs1companyprefix"
	case ItemReference:
		return "itemref"
	case SerialReference:
		return "serialref"
	case Serial:
		return "serial"
	case AssetType:
		return "assettype"
	case IndividualAssetReference:
		return "indassetref"
	case DataIdentifier:
		return "di"
	case OwnerCode:
		return "ownercode"
	case EquipmentCategory:
		return "equipmentcategory"
	case ContainerSerial:
		return "containerserial"
	case CheckDigit:
		return "checkdigit"
	case ISOIdentifier:
		return "identifier"
	case FilterValue:
		return "filter"
	case Partition:
		return "partition"
	default:
		return "unknown"
	}
}

// Component is one named field value.
type Component struct {
	Name  ComponentName
	Value string
}

// Errors returned when constructing identities.
var (
	ErrUnknownKeyType  = errors.New("identity: unknown key type")
	ErrComponentLayout = errors.New("identity: components do not match the canonical order")
)

// componentOrder fixes the canonical component order per key type. Pattern
// prefix semantics are defined against this order, so it is part of the
// contract that internal/filter depends on.
var componentOrder = map[KeyType][]ComponentName{
	KeyTypeSGTIN:            {GS1CompanyPrefix, ItemReference, Serial},
	KeyTypeSSCC:             {GS1CompanyPrefix, SerialReference},
	KeyTypeGRAI:             {GS1CompanyPrefix, AssetType, Serial},
	KeyTypeGIAI:             {GS1CompanyPrefix, IndividualAssetReference},
	KeyTypeFreightContainer: {OwnerCode, EquipmentCategory, ContainerSerial, CheckDigit},
	KeyTypeTransportUnit:    {DataIdentifier, ISOIdentifier},
}

// ComponentOrder returns the canonical component order for a key type.
func ComponentOrder(k KeyType) []ComponentName {
	order, ok := componentOrder[k]
	if !ok {
		return nil
	}
	return append([]ComponentName(nil), order...)
}

// Value is an immutable canonical identity.
type Value struct {
	authority  Authority
	keyType    KeyType
	components []Component
}

// New builds a Value. The components must be exactly the canonical order for
// the key type; nothing is reordered or defaulted, because a translator that
// guesses at missing fields hides encoding bugs.
//
// Component values are not inspected here: what a field may contain is a rule
// of the standard that produced it, and enforcing it belongs to the translating
// package. An empty value is legitimate where a standard fixes a field at zero
// length, as GRAI-96 does for the asset type of a twelve-digit company prefix.
func New(authority Authority, keyType KeyType, components []Component) (Value, error) {
	order, ok := componentOrder[keyType]
	if !ok {
		return Value{}, fmt.Errorf("%w: %d", ErrUnknownKeyType, keyType)
	}
	if len(components) != len(order) {
		return Value{}, fmt.Errorf("%w: got %d components, want %d for %s",
			ErrComponentLayout, len(components), len(order), keyType)
	}
	cloned := make([]Component, len(components))
	for i, c := range components {
		if c.Name != order[i] {
			return Value{}, fmt.Errorf("%w: position %d is %s, want %s", ErrComponentLayout, i, c.Name, order[i])
		}
		cloned[i] = c
	}
	return Value{authority: authority, keyType: keyType, components: cloned}, nil
}

// Authority returns the numbering authority.
func (v Value) Authority() Authority { return v.authority }

// KeyType returns the business key type.
func (v Value) KeyType() KeyType { return v.keyType }

// Len returns the number of components.
func (v Value) Len() int { return len(v.components) }

// At returns the component at index i in canonical order.
func (v Value) At(i int) Component { return v.components[i] }

// Components iterates the components in canonical order without copying.
func (v Value) Components() iter.Seq[Component] {
	return func(yield func(Component) bool) {
		for _, c := range v.components {
			if !yield(c) {
				return
			}
		}
	}
}

// Component returns the named component.
func (v Value) Component(name ComponentName) (Component, bool) {
	for _, c := range v.components {
		if c.Name == name {
			return c, true
		}
	}
	return Component{}, false
}

// IsZero reports whether the value carries no identity.
func (v Value) IsZero() bool { return v.keyType == KeyTypeUnknown && len(v.components) == 0 }

// Equal reports exact structural equality.
func (v Value) Equal(other Value) bool {
	if v.authority != other.authority || v.keyType != other.keyType || len(v.components) != len(other.components) {
		return false
	}
	for i := range v.components {
		if v.components[i] != other.components[i] {
			return false
		}
	}
	return true
}

// Key is a comparable map key for a Value.
//
// It is opaque and build-local: its encoding is not stable across versions and
// it must never be rendered, logged as an identifier, persisted, or placed on a
// wire. Equality is exact — the encoding is length-prefixed, not hashed — so
// Key equality and Equal always agree.
type Key struct {
	authority Authority
	keyType   KeyType
	fields    string
}

// Key returns the comparable key for this value.
func (v Value) Key() Key {
	var b strings.Builder
	for _, c := range v.components {
		b.WriteByte(byte(c.Name))
		b.WriteString(strconv.Itoa(len(c.Value)))
		b.WriteByte(':')
		b.WriteString(c.Value)
	}
	return Key{authority: v.authority, keyType: v.keyType, fields: b.String()}
}

// Describe renders a human-readable form for logs and CLI output.
//
// It is diagnostic only: unstable across versions, never parsed back, and never
// an interchange format. Standards-backed textual forms live with their
// standard's package.
func (v Value) Describe() string {
	var b strings.Builder
	b.WriteString(v.keyType.String())
	b.WriteByte('[')
	for i, c := range v.components {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(c.Name.String())
		b.WriteByte('=')
		b.WriteString(c.Value)
	}
	b.WriteByte(']')
	return b.String()
}
