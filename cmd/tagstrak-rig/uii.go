package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
	"github.com/iomz/tagstrak/v2/internal/tdt/iso"
)

const uiiUsage = `Usage: tagstrak-rig uii epc --scheme SCHEME [flags]
       tagstrak-rig uii iso17360 --agency IAC [flags]
       tagstrak-rig uii iso17363 [flags]

Prints PC,EPC for one generated tag, or PATTERN,PREFIX-BITS with --pattern.
Fields left out are generated at random; with --pattern they become stars.`

// uiiOptions holds every uii flag. Each kind reads only its own fields.
type uiiOptions struct {
	pattern bool
	format  string

	// epc
	scheme, filter, companyPrefix, itemRef, serialRef, serial, assetType, assetRef string

	// iso17360 and iso17363
	dataIdentifier, agency, company, isoSerial string
	owner, category, containerSerial           string
}

// tag is a generated tag, or a pattern and the bit prefix it pins.
type tag struct {
	bits   []byte
	bitLen int
	pc     uint16

	pattern string
}

// uii runs the uii subcommand: args are what follows "uii".
func uii(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stderr, uiiUsage)
		return flag.ErrHelp
	}
	kind := args[0]
	var o uiiOptions
	flags := flag.NewFlagSet("tagstrak-rig uii "+kind, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&o.pattern, "pattern", false, "Print a pattern and the bit prefix it pins instead of a tag")
	flags.StringVar(&o.format, "format", "hex", "Tag output: hex or bin")
	switch kind {
	case "epc":
		flags.StringVar(&o.scheme, "scheme", "", "EPC scheme, such as sgtin-96 (required)")
		flags.StringVar(&o.filter, "filter", "3", "Filter value")
		flags.StringVar(&o.companyPrefix, "company-prefix", "", "GS1 company prefix, such as 0614141; random 7 digits if omitted")
		flags.StringVar(&o.itemRef, "item-ref", "", "[sgtin] Item reference")
		flags.StringVar(&o.serial, "serial", "", "[sgtin, grai] Serial")
		flags.StringVar(&o.serialRef, "serial-ref", "", "[sscc] Serial reference")
		flags.StringVar(&o.assetType, "asset-type", "", "[grai] Asset type")
		flags.StringVar(&o.assetRef, "asset-ref", "", "[giai] Individual asset reference")
	case "iso17360":
		flags.StringVar(&o.dataIdentifier, "data-identifier", "J", "MH10.8.2 data identifier: J, 1J, 2J, 3J, 4J, 5J or 6J")
		flags.StringVar(&o.agency, "agency", "", "Issuing agency code, such as UN (required)")
		flags.StringVar(&o.company, "company", "", "Company identification, such as 043325711")
		flags.StringVar(&o.isoSerial, "serial", "", "Serial number")
	case "iso17363":
		flags.StringVar(&o.owner, "owner", "", "Three-letter BIC owner code; random if omitted")
		flags.StringVar(&o.category, "category", "U", "ISO 6346 equipment category: U, J or Z")
		flags.StringVar(&o.containerSerial, "serial", "", "Six-digit container serial; random if omitted")
	default:
		fmt.Fprintln(stderr, uiiUsage)
		return fmt.Errorf("unknown uii kind %q", kind)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if o.format != "hex" && o.format != "bin" {
		return fmt.Errorf("--format must be hex or bin, not %q", o.format)
	}

	var t tag
	var err error
	switch kind {
	case "epc":
		t, err = makeEPC(o)
	case "iso17360":
		t, err = makeISO(o, iso.SchemeTransportUnit)
	case "iso17363":
		t, err = makeISO(o, iso.Scheme17363)
	}
	if err != nil {
		return err
	}
	if o.pattern {
		_, err = fmt.Fprintf(stdout, "%s,%s\n", t.pattern, bitString(t.bits, t.bitLen))
		return err
	}
	body := hex.EncodeToString(t.bits)
	if o.format == "bin" {
		body = bitString(t.bits, t.bitLen)
	}
	_, err = fmt.Fprintf(stdout, "%04x,%s\n", t.pc, body)
	return err
}

// epcField returns the flag value for one identity component.
func (o uiiOptions) epcField(name identity.ComponentName) string {
	switch name {
	case identity.GS1CompanyPrefix:
		return o.companyPrefix
	case identity.ItemReference:
		return o.itemRef
	case identity.SerialReference:
		return o.serialRef
	case identity.Serial:
		return o.serial
	case identity.AssetType:
		return o.assetType
	case identity.IndividualAssetReference:
		return o.assetRef
	default:
		return ""
	}
}

// makeEPC builds an EPC from the supplied and generated field values.
//
// In pattern mode the first unsupplied field and everything after it become
// stars, which is how a pattern names a set of identities rather than one.
func makeEPC(o uiiOptions) (tag, error) {
	if o.scheme == "" {
		return tag{}, errors.New("--scheme is required")
	}
	scheme := identity.SchemeID(strings.ToLower(o.scheme))
	keyType, err := tdt.KeyTypeOf(scheme)
	if err != nil {
		return tag{}, err
	}
	if o.companyPrefix == "" {
		o.companyPrefix = randomDigits(7)
	}
	specs, err := tdt.GS1FieldSpecs(scheme, len(o.companyPrefix))
	if err != nil {
		return tag{}, err
	}
	filter, err := strconv.ParseUint(o.filter, 10, 8)
	if err != nil {
		return tag{}, fmt.Errorf("invalid filter value %q", o.filter)
	}

	values := make([]string, len(specs))
	star := false
	for i, spec := range specs {
		supplied := o.epcField(spec.Name)
		switch {
		case star:
			values[i] = "*"
		case supplied == "" && o.pattern:
			star, values[i] = true, "*"
		case supplied == "":
			values[i] = randomField(spec)
		default:
			values[i] = normalise(spec, supplied)
		}
	}

	if o.pattern {
		text := fmt.Sprintf("urn:epc:pat:%s:%d.%s", scheme, filter, strings.Join(values, "."))
		p, err := tdt.CompilePattern(text)
		if err != nil {
			return tag{}, err
		}
		bits, bitLen := prefixBits(p)
		return tag{bits: bits, bitLen: bitLen, pattern: text}, nil
	}

	comps := make([]identity.Component, len(specs))
	for i, spec := range specs {
		comps[i] = identity.Component{Name: spec.Name, Value: values[i]}
	}
	value, err := identity.New(identity.AuthorityGS1, keyType, comps)
	if err != nil {
		return tag{}, err
	}
	translation, err := tdt.EncodeGS1(value, scheme, uint8(filter))
	if err != nil {
		return tag{}, err
	}
	return encodedTag(translation), nil
}

// makeISO builds an ISO UII for a supported construct.
func makeISO(o uiiOptions, scheme identity.SchemeID) (tag, error) {
	var comps []identity.Component
	var authority identity.Authority
	var keyType identity.KeyType

	switch scheme {
	case iso.Scheme17363:
		// AFI 0xA9 is authoritatively reserved for ISO 17363, and the ISO 6346
		// container code below is well defined, but how an ISO 17363 UII
		// carries that code is not established by the available standards
		// evidence. tagstrak will not write one. See issue #8 Amendment 1,
		// decision A7.
		owner := strings.ToUpper(o.owner)
		if owner == "" {
			owner = randomLetters(3)
		}
		serial := o.containerSerial
		if serial == "" {
			serial = randomDigits(6)
		}
		check, err := iso.CheckDigit6346(owner + o.category + serial)
		if err != nil {
			return tag{}, err
		}
		return tag{}, fmt.Errorf("ISO 17363 container %s%s%s%s cannot be written: its UII carriage is not established by the available standards evidence",
			owner, o.category, serial, check)
	case iso.SchemeTransportUnit:
		authority, keyType = identity.AuthorityISO15459, identity.KeyTypeTransportUnit
		if o.agency == "" {
			return tag{}, errors.New("an ISO/IEC 17360 transport unit needs at least --agency")
		}
		comps = []identity.Component{
			{Name: identity.DataIdentifier, Value: o.dataIdentifier},
			{Name: identity.ISOIdentifier, Value: strings.ToUpper(o.agency + o.company + o.isoSerial)},
		}
	default:
		return tag{}, fmt.Errorf("unsupported ISO scheme %s", scheme)
	}

	value, err := identity.New(authority, keyType, comps)
	if err != nil {
		return tag{}, err
	}
	translation, err := tdt.EncodeISO(value, scheme)
	if err != nil {
		return tag{}, err
	}
	t := encodedTag(translation)
	if o.pattern {
		// ISO publishes no pattern grammar, so a pattern is expressed as the
		// bit prefix the identity occupies rather than as an invented URI.
		t.pattern = iso.Display(value)
	}
	return t, nil
}

// encodedTag returns the tag bits and its protocol-control word.
func encodedTag(translation tdt.Translation) tag {
	bits, bitLen := translation.Encoding.Bits()
	return tag{bits: bits, bitLen: bitLen, pc: tdt.ProtocolControl(translation.Encoding)}
}

// prefixBits returns the contiguous leading bits a pattern pins, which is
// what a prefix matcher consumes.
func prefixBits(p identity.Pattern) ([]byte, int) {
	bits := p.Bits()
	if bits == nil {
		return nil, 0
	}
	value, mask := bits.Value(), bits.Mask()
	n := 0
	for n < bits.BitLen() && mask[n/8]>>(7-uint(n%8))&1 == 1 {
		n++
	}
	return value, n
}

func bitString(data []byte, bitLen int) string {
	var b strings.Builder
	for i := 0; i < bitLen; i++ {
		b.WriteByte('0' + data[i/8]>>(7-uint(i%8))&1)
	}
	return b.String()
}

// randomField generates a value the encoding can carry.
func randomField(spec tdt.FieldSpec) string {
	if spec.Fixed {
		return randomDigits(spec.Length)
	}
	// One digit fewer than the maximum always fits, with a nonzero lead.
	digits := len(strconv.FormatUint(spec.Max, 10)) - 1
	if digits < 1 {
		digits = 1
	}
	return string(rune('1'+rand.IntN(9))) + randomDigits(digits-1)
}

func randomDigits(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('0' + rand.IntN(10))
	}
	return string(b)
}

func randomLetters(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('A' + rand.IntN(26))
	}
	return string(b)
}

// normalise pads a supplied value to the field's declared width.
func normalise(spec tdt.FieldSpec, value string) string {
	if !spec.Fixed || len(value) >= spec.Length {
		return value
	}
	return strings.Repeat("0", spec.Length-len(value)) + value
}
