package iso

import (
	"fmt"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

// identityLimit is the maximum length of an ISO/IEC 15459 identity, qualifier
// included: "The identity for individual transport units shall not contain
// more than 50 characters" (ISO/IEC 15459-1:2014, 5.2).
const identityLimit = 50

// authorities maps an ANS MH10.8.2 data identifier to the body whose numbering
// system defines the identity it qualifies.
//
// Authority is a property of the data identifier, never of the AFI. ISO/IEC
// 15459 participates per data identifier: 25S, 25T, 25B and 5R are
// definitionally IAC + CIN + SN, and the "J" licence plate series is an
// ISO/IEC 15459 identity by reference (ANS MH10.8.2 note 6; ISO/IEC
// 15459-1:2014 clause 4). ISO/IEC 17360 is an application standard and is not
// a numbering authority at all (issue #8 Amendment 2, decision B5).
//
// Deliberately absent, because their numbering authority is outside the
// supported baseline rather than unknown: "I" is a vehicle identification
// number under ISO 3779, "1B" is a container code assigned by the container
// owner or a regulatory agency, and "7J" is a government vehicle registration
// plate that ANS MH10.8.2 notes is not unique worldwide. A data identifier
// absent from this table is reported as unsupported, not as invalid.
var authorities = map[string]identity.Authority{
	"J":   identity.AuthorityISO15459,
	"1J":  identity.AuthorityISO15459,
	"2J":  identity.AuthorityISO15459,
	"3J":  identity.AuthorityISO15459,
	"4J":  identity.AuthorityISO15459,
	"5J":  identity.AuthorityISO15459,
	"6J":  identity.AuthorityISO15459,
	"25S": identity.AuthorityISO15459,
	"25T": identity.AuthorityISO15459,
	"25B": identity.AuthorityISO15459,
	"5R":  identity.AuthorityISO15459,
	"7B":  identity.AuthorityISO6346,
}

// AuthorityForDI returns the numbering authority a data identifier qualifies
// an identity under.
func AuthorityForDI(di string) (identity.Authority, bool) {
	a, ok := authorities[di]
	return a, ok
}

// Interpret turns decoded characters into a canonical identity, using the
// payload rules of the application the AFI names.
//
// This is the only stage that needs application-specific evidence, and it is
// where a construct whose payload rules are not established is refused.
func Interpret(c Construct, t Text) (identity.Value, string, error) {
	scheme, err := c.Scheme()
	if err != nil {
		return identity.Value{}, "", err
	}
	if err := checkControls(scheme, t); err != nil {
		return identity.Value{}, "", err
	}
	// Residue bits mean the UII does not end on a character boundary. Whether
	// they hold a truncated terminator or nothing is not established, so they
	// are surfaced rather than resolved (issue #19 gates U1, U10).
	if t.ResidueBits != 0 {
		return identity.Value{}, "", fmt.Errorf("%w: %s carries %d residue bits after the last complete 6-bit code",
			tdtcore.ErrUnresolvedSemantics, scheme, t.ResidueBits)
	}
	switch scheme {
	case SchemeTransportUnit:
		return interpretTransportUnit(c, t.Characters)
	case Scheme17363:
		// AFI 0xA9 and 0xAA are authoritatively reserved for ISO 17363, and
		// the ISO 6346 container code is independently well defined, but the
		// register assigns ISO 17363 no data construct, ISO/IEC 15962 covers
		// only ISO 17364, 17365, 17366 and 17367 in its data identifier direct
		// encoding clauses, and ISO 17363's own air interface is 433 MHz or
		// 2,45 GHz rather than ISO/IEC 18000-63. Recognition stands;
		// promotion does not (issue #8 Amendment 2, decision B8).
		return identity.Value{}, "", fmt.Errorf("%w: ISO 17363 UII carriage over Gen2 is not established by the available standards evidence",
			tdtcore.ErrUnresolvedSemantics)
	default:
		return identity.Value{}, "", fmt.Errorf("%w: %s", tdtcore.ErrUnsupportedScheme, scheme)
	}
}

// checkControls rejects a decoded stream that carries structural control
// characters.
//
// GS and EOT have established roles: GS separates data identifiers and EOT
// terminates the message (ISO/IEC 17360:2023, 5.4.3). What is NOT established
// for the 6-bit constructs is the layout those roles imply -- the
// complete/incomplete-EOT rule is normative only for the AFI 0xAD binary
// bitstream, and extending it here is Derived. So their presence is surfaced
// as unresolved rather than acted on (issue #8 Amendment 2, decision B2).
//
// RS, FS and US have no role in an ISO/IEC 17360 UII at all, so they are a
// structural defect in the data rather than an evidence gap.
func checkControls(scheme identity.SchemeID, t Text) error {
	for _, ctl := range t.Controls {
		switch ctl.Code {
		case CodeGS, CodeEOT:
			return fmt.Errorf("%w: %s carries %s at character %d, and the layout it implies is not established for 6-bit constructs",
				tdtcore.ErrUnresolvedSemantics, scheme, ctl.Name, ctl.Index)
		default:
			return tdtcore.Field(string(scheme), "uii", "control", ctl.Name,
				"ISO/IEC 17360:2023 5.4.3 gives no role to this control in a 6-bit UII")
		}
	}
	return nil
}

// interpretTransportUnit splits the leading data identifier from the
// identifier it prefixes and validates the identifier against ISO/IEC 15459.
//
// The registered data identifier sets are a RECOGNITION table, not a
// validation rule. The register edition that published them has been
// superseded by one that removed the table entirely, while ISO/IEC 17360:2023
// 5.2 permits any applicable ISO/IEC 15418 data identifier. A well formed data
// identifier absent from the table is therefore unsupported, not invalid
// (issue #8 Amendment 2, decision B6; issue #19 gate U6).
func interpretTransportUnit(c Construct, text string) (identity.Value, string, error) {
	di, body, ok := splitRegisteredDI(c, text)
	if !ok {
		return identity.Value{}, "", fmt.Errorf("%w: no data identifier registered to AFI 0x%02X prefixes %q; recognised set is %s",
			tdtcore.ErrUnsupportedScheme, c.AFI, text, strings.Join(registeredDIs(c), ", "))
	}
	authority, ok := AuthorityForDI(di)
	if !ok {
		return identity.Value{}, "", fmt.Errorf("%w: no numbering authority is established for data identifier %q",
			tdtcore.ErrUnsupportedScheme, di)
	}
	if err := checkISO15459Identity(SchemeTransportUnit, di, body); err != nil {
		return identity.Value{}, "", err
	}
	value, err := identity.New(authority, identity.KeyTypeTransportUnit, []identity.Component{
		{Name: identity.DataIdentifier, Value: di},
		{Name: identity.ISOIdentifier, Value: body},
	})
	return value, di, err
}

// checkISO15459Identity applies the two constraints ISO/IEC 15459-1:2014
// places on a transport unit identity.
//
// 5.2 caps the identity, qualifier included, at 50 characters. 5.3 restricts
// it to "upper-case alphabetic characters and numeric digits of the invariant
// character set of ISO/IEC 646", which is narrower than the common rule in
// ISO/IEC 15459-3:2014 6.3 and governs for transport units.
//
// The identity is NOT decomposed further. An Issuing Agency Code is prefix
// decodable from its first character, but the boundary between the company
// identifying number and the serial number is defined per Issuing Agency and
// is not decidable from the encoded string (ISO/IEC 15459-3:2014 6.1 e). The
// decidable decomposition is the qualifier and the remainder, and that is what
// is produced (issue #8 Amendment 2, gate G10).
func checkISO15459Identity(scheme identity.SchemeID, di, body string) error {
	if body == "" {
		return tdtcore.Field(string(scheme), "identifier", "empty", body, "ISO/IEC 15459-1:2014 clause 5")
	}
	if n := len(di) + len(body); n > identityLimit {
		return &tdtcore.FieldError{
			Scheme: string(scheme), Field: "identifier", Rule: "length", Value: di + body,
			Spec: "ISO/IEC 15459-1:2014 5.2: at most 50 characters", Err: tdtcore.ErrLengthMismatch,
		}
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') {
			continue
		}
		return tdtcore.Field(string(scheme), "identifier", "charset", body,
			"ISO/IEC 15459-1:2014 5.3: upper-case alphabetic and numeric only")
	}
	return nil
}

// splitRegisteredDI matches the longest registered data identifier that
// prefixes the text, so "1J" wins over "J" where both are registered.
func splitRegisteredDI(c Construct, text string) (string, string, bool) {
	best := ""
	for _, d := range c.DataIdentifiers {
		if len(d.DI) > len(best) && strings.HasPrefix(text, d.DI) {
			best = d.DI
		}
	}
	if best == "" {
		return "", "", false
	}
	return best, text[len(best):], true
}

func registeredDIs(c Construct) []string {
	out := make([]string, 0, len(c.DataIdentifiers))
	for _, d := range c.DataIdentifiers {
		out = append(out, d.DI)
	}
	return out
}

// InterpretContainerCode parses an ISO 6346 container code into a canonical
// identity: three owner letters, one equipment category letter, a six digit
// serial and a modulus 11 check digit. This is the structure ISO 17363:2013
// 5.2 and ANS MH10.8.2 data identifier "7B" both specify, and the equipment
// category letters U, J and Z are those published by BIC, the registrar.
//
// It is exported and tested because the container code itself is well defined,
// but translation does not reach it: how an ISO 17363 UII carries this code
// over Gen2 is not established (issue #8 Amendment 2, decision B8; issue #19
// gate U3). A caller who knows their own tag layout can apply it; tagstrak
// does not claim that layout on their behalf.
func InterpretContainerCode(code string) (identity.Value, error) {
	const scheme = string(Scheme17363)
	if len(code) != 11 {
		return identity.Value{}, tdtcore.Field(scheme, "containercode", "length", code, "ISO 6346 clause 4")
	}
	owner, category, serial, check := code[:3], code[3:4], code[4:10], code[10:]
	for i := 0; i < len(owner); i++ {
		if owner[i] < 'A' || owner[i] > 'Z' {
			return identity.Value{}, tdtcore.Field(scheme, "ownercode", "charset", owner, "ISO 6346 clause 4")
		}
	}
	switch category {
	case "U", "J", "Z":
	default:
		return identity.Value{}, tdtcore.Field(scheme, "equipmentcategory", "value", category, "ISO 6346 clause 4")
	}
	for i := 0; i < len(serial); i++ {
		if serial[i] < '0' || serial[i] > '9' {
			return identity.Value{}, tdtcore.Field(scheme, "containerserial", "charset", serial, "ISO 6346 clause 4")
		}
	}
	want, err := CheckDigit6346(owner + category + serial)
	if err != nil {
		return identity.Value{}, err
	}
	if check != want {
		return identity.Value{}, &tdtcore.FieldError{
			Scheme: scheme, Field: "checkdigit", Rule: "mismatch", Value: check,
			Spec: "ISO 6346 clause 4.3", Err: tdtcore.ErrCheckDigit,
		}
	}
	return identity.New(identity.AuthorityISO6346, identity.KeyTypeFreightContainer, []identity.Component{
		{Name: identity.OwnerCode, Value: owner},
		{Name: identity.EquipmentCategory, Value: category},
		{Name: identity.ContainerSerial, Value: serial},
		{Name: identity.CheckDigit, Value: check},
	})
}

// CheckDigit6346 computes the ISO 6346 check digit for the ten characters of
// an owner code, equipment category and serial number.
//
// Letters take the values A=10 through Z=38 with multiples of eleven skipped,
// each character is weighted by 2^position, and the sum modulo eleven is the
// check digit, with ten written as zero.
//
// DERIVED, not normative. The governing text is ISO 6346:1995 Annex A, cited
// by name from ISO 17363:2013 5.2 and from ANS MH10.8.2 data identifier "7B",
// and it is not publicly readable. The rule implemented here is the standard
// published one and reproduces the canonical worked example CSQU3054383, but
// it must not be presented as normative (issue #8 Amendment 2, gate G7; issue
// #19 gate U11).
func CheckDigit6346(code string) (string, error) {
	if len(code) != 10 {
		return "", tdtcore.Field(string(Scheme17363), "containercode", "length", code, "ISO 6346 clause 4.3")
	}
	sum, weight := 0, 1
	for i := 0; i < 10; i++ {
		v, err := characterValue6346(code[i])
		if err != nil {
			return "", err
		}
		sum += v * weight
		weight *= 2
	}
	return string(rune('0' + sum%11%10)), nil
}

// values6346 assigns A=10 through Z=38, skipping every multiple of eleven.
var values6346 = func() [26]int {
	var out [26]int
	v := 10
	for i := range out {
		for v%11 == 0 {
			v++
		}
		out[i] = v
		v++
	}
	return out
}()

func characterValue6346(c byte) (int, error) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), nil
	case c >= 'A' && c <= 'Z':
		return values6346[c-'A'], nil
	default:
		return 0, tdtcore.Field(string(Scheme17363), "containercode", "charset", string(c), "ISO 6346 clause 4.3")
	}
}
