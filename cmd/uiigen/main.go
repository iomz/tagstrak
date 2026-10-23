// Command uiigen generates arbitrary UIIs (EPC and ISO) for test data.
//
// Every encoding decision comes from the translation substrate in
// internal/tdt, which is generated from pinned GS1 TDT artefacts. This command
// owns only value selection and output formatting; it holds no partition
// tables or scheme layouts of its own.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/binutil"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
	"github.com/iomz/tagstrak/v2/internal/tdt/iso"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	app          = kingpin.New("uiigen", "A tool to generate an arbitrary UII (aka EPC).")
	prefixFilter = app.Flag("pf", "Print a pattern and its bit prefix for the given parameters").Default("false").Bool()
	modeHex      = app.Flag("hex", "Print the ID in Hex.").Default("false").Bool()
	modeDec      = app.Flag("dec", "Print the ID in Dec.").Default("false").Bool()

	epc                         = app.Command("epc", "Generate an EPC.")
	epcScheme                   = epc.Flag("cs", "EPC coding scheme. ex.) sgtin-96").String()
	epcFilter                   = epc.Flag("filter", "Filter Value for EPC.").Default("3").String()
	epcCompanyPrefix            = epc.Flag("cp", "[GIAI,GRAI,SGTIN,SSCC] Company Prefix for EPC. ex.) 0614141").String()
	epcItemReference            = epc.Flag("ir", "[SGTIN] Item Reference Value for EPC.").String()
	epcExtension                = epc.Flag("ext", "[SSCC] Extension value for EPC.").String()
	epcSerial                   = epc.Flag("ser", "[GRAI,SGTIN] Serial value for EPC.").String()
	epcIndivisualAssetReference = epc.Flag("iar", "[GIAI] Indivisual Asset Reference value for EPC.").String()
	epcAssetType                = epc.Flag("at", "[GRAI] Asset Type for EPC.").String()

	isoCmd                         = app.Command("iso", "Generate an ISO UII.")
	iso17363                       = isoCmd.Command("17363", "ISO 17363 freight containers (recognition and decoding only; writing is not supported).")
	isoOwnerCode                   = iso17363.Flag("oc", "A three letter container owner code (OC) assigned in cooperation with the Bureau International des Containers et du Transport Intermodal(BIC). ex.) CSQ").String()
	isoEquipmentCategoryIdentifier = iso17363.Flag("ei", "A one letter equipment category identifier (EI) in ISO 6346 = U, J or Z.").Default("U").String()
	isoContainerSerialNumber       = iso17363.Flag("csn", "A six digit serial number (CSN). ex.) 305438").String()
	iso17360                       = isoCmd.Command("17360", "ISO/IEC 17360 transport unit (AFI 0xA2).")
	isoDataIdeintifier             = iso17360.Flag("di", "Registered MH10.8.2 data identifier: J, 1J, 2J, 3J, 4J, 5J or 6J.").Default("J").String()
	isoIssuingAgencyCode           = iso17360.Flag("iac", "Issuing Agency Code. ex.) UN or OD").String()
	isoCompanyIdentification       = iso17360.Flag("cin", "Company Identification. ex.) 043325711 or CIN1").String()
	isoSerialNumber                = iso17360.Flag("sn", "Serial Number for ISO UII. ex.) MH8031200000000001").String()
)

// epcFields collects the command-line value for each identity component.
func epcFields(name identity.ComponentName) string {
	switch name {
	case identity.GS1CompanyPrefix:
		return *epcCompanyPrefix
	case identity.ItemReference:
		return *epcItemReference
	case identity.SerialReference:
		return *epcExtension
	case identity.Serial:
		return *epcSerial
	case identity.AssetType:
		return *epcAssetType
	case identity.IndividualAssetReference:
		return *epcIndivisualAssetReference
	default:
		return ""
	}
}

// makeEPC builds an EPC from the supplied and generated field values.
//
// In pattern mode the first unsupplied field and everything after it become
// stars, which is how a pattern names a set of identities rather than one.
func makeEPC(pattern bool) (string, string, error) {
	scheme := identity.SchemeID(strings.ToLower(*epcScheme))
	if _, err := tdt.KeyTypeOf(scheme); err != nil {
		return "", "", err
	}
	gcp := *epcCompanyPrefix
	if gcp == "" {
		gcp = binutil.GenerateNLengthDigitString(7)
	}
	specs, err := tdt.GS1FieldSpecs(scheme, len(gcp))
	if err != nil {
		return "", "", err
	}
	filter, err := strconv.ParseUint(*epcFilter, 10, 8)
	if err != nil {
		return "", "", fmt.Errorf("invalid filter value %q", *epcFilter)
	}

	values := make([]string, len(specs))
	star := false
	for i, spec := range specs {
		supplied := epcFields(spec.Name)
		if spec.Name == identity.GS1CompanyPrefix {
			supplied = gcp
		}
		switch {
		case star:
			values[i] = "*"
		case supplied == "" && pattern:
			star, values[i] = true, "*"
		case supplied == "":
			values[i] = randomField(spec)
		default:
			values[i] = normalise(spec, supplied)
		}
	}

	if pattern {
		text := fmt.Sprintf("urn:epc:pat:%s:%d.%s", scheme, filter, strings.Join(values, "."))
		p, err := tdt.CompilePattern(text)
		if err != nil {
			return "", "", err
		}
		return prefixBits(p), text, nil
	}

	comps := make([]identity.Component, len(specs))
	for i, spec := range specs {
		comps[i] = identity.Component{Name: spec.Name, Value: values[i]}
	}
	keyType, err := tdt.KeyTypeOf(scheme)
	if err != nil {
		return "", "", err
	}
	value, err := identity.New(identity.AuthorityGS1, keyType, comps)
	if err != nil {
		return "", "", err
	}
	translation, err := tdt.EncodeGS1(value, scheme, uint8(filter))
	if err != nil {
		return "", "", err
	}
	return encodedTag(translation)
}

// makeISO builds an ISO UII for a supported construct.
func makeISO(scheme identity.SchemeID, pattern bool) (string, string, error) {
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
		owner := strings.ToUpper(*isoOwnerCode)
		if owner == "" {
			owner = binutil.GenerateNLengthAlphabetString(3)
		}
		serial := *isoContainerSerialNumber
		if serial == "" {
			serial = binutil.GenerateNLengthDigitString(6)
		}
		check, err := iso.CheckDigit6346(owner + *isoEquipmentCategoryIdentifier + serial)
		if err != nil {
			return "", "", err
		}
		return "", "", fmt.Errorf("ISO 17363 container %s%s%s%s cannot be written: its UII carriage is not established by the available standards evidence",
			owner, *isoEquipmentCategoryIdentifier, serial, check)
	case iso.SchemeTransportUnit:
		authority, keyType = identity.AuthorityISO15459, identity.KeyTypeTransportUnit
		body := strings.ToUpper(*isoIssuingAgencyCode + *isoCompanyIdentification + *isoSerialNumber)
		if body == "" {
			return "", "", fmt.Errorf("an ISO/IEC 17360 transport unit needs at least an issuing agency code")
		}
		comps = []identity.Component{
			{Name: identity.DataIdentifier, Value: *isoDataIdeintifier},
			{Name: identity.ISOIdentifier, Value: body},
		}
	default:
		return "", "", fmt.Errorf("unsupported ISO scheme %s", scheme)
	}

	value, err := identity.New(authority, keyType, comps)
	if err != nil {
		return "", "", err
	}
	translation, err := tdt.EncodeISO(value, scheme)
	if err != nil {
		return "", "", err
	}
	if pattern {
		// ISO publishes no pattern grammar, so a pattern is expressed as the
		// bit prefix the identity occupies rather than as an invented URI.
		bits, bitLen := translation.Encoding.Bits()
		return bitString(bits, bitLen), iso.Display(value), nil
	}
	return encodedTag(translation)
}

// encodedTag renders the tag bits and its protocol-control word.
func encodedTag(translation tdt.Translation) (string, string, error) {
	bits, bitLen := translation.Encoding.Bits()
	pc := tdt.ProtocolControl(translation.Encoding)
	return bitString(bits, bitLen), fmt.Sprintf("%04x", pc), nil
}

// prefixBits renders the contiguous leading bits a pattern pins, which is what
// a prefix matcher consumes.
func prefixBits(p identity.Pattern) string {
	bits := p.Bits()
	if bits == nil {
		return ""
	}
	value, mask := bits.Value(), bits.Mask()
	var b strings.Builder
	for i := 0; i < bits.BitLen(); i++ {
		if mask[i/8]>>(7-uint(i%8))&1 == 0 {
			break
		}
		if value[i/8]>>(7-uint(i%8))&1 == 1 {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

func bitString(data []byte, bitLen int) string {
	var b strings.Builder
	for i := 0; i < bitLen; i++ {
		if data[i/8]>>(7-uint(i%8))&1 == 1 {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

// randomField generates a value the encoding can carry.
func randomField(spec tdt.FieldSpec) string {
	if spec.Fixed && spec.Length == 0 {
		return ""
	}
	if spec.Fixed {
		return binutil.GenerateNLengthDigitString(spec.Length)
	}
	digits := len(strconv.FormatUint(spec.Max, 10)) - 1
	if digits < 1 {
		digits = 1
	}
	return strconv.FormatUint(uint64(binutil.GenerateRandomInt(0, 9))*pow10(digits-1)+
		uint64(binutil.GenerateRandomInt(0, int(pow10(digits-1))-1)), 10)
}

func pow10(n int) uint64 {
	v := uint64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

// normalise pads a supplied value to the field's declared width.
func normalise(spec tdt.FieldSpec, value string) string {
	if !spec.Fixed || len(value) >= spec.Length {
		return value
	}
	return strings.Repeat("0", spec.Length-len(value)) + value
}

func main() {
	parse := kingpin.MustParse(app.Parse(os.Args[1:]))

	var bs, opt string
	var err error
	switch parse {
	case epc.FullCommand():
		bs, opt, err = makeEPC(*prefixFilter)
	case iso17363.FullCommand():
		bs, opt, err = makeISO(iso.Scheme17363, *prefixFilter)
	case iso17360.FullCommand():
		bs, opt, err = makeISO(iso.SchemeTransportUnit, *prefixFilter)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(bs) == 0 {
		return
	}
	switch {
	case *prefixFilter:
		fmt.Println(opt + "," + bs)
	case *modeHex:
		hs, _ := binutil.ParseBinStringToHexString(bs)
		fmt.Println(opt + "," + hs)
	case *modeDec:
		ds, _ := binutil.ParseBinStringToDecArrayString(bs)
		pc, _ := binutil.ParseHexStringToDecArrayString(opt)
		fmt.Println("[]byte{" + pc + "},[]byte{" + ds + "}")
	default:
		fmt.Println(opt + "," + bs)
	}
}
