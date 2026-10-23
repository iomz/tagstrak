package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/binutil"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

func reset() {
	for _, p := range []*string{
		epcScheme, epcCompanyPrefix, epcItemReference, epcExtension, epcSerial,
		epcIndivisualAssetReference, epcAssetType, isoOwnerCode, isoContainerSerialNumber,
		isoIssuingAgencyCode, isoCompanyIdentification, isoSerialNumber,
	} {
		*p = ""
	}
	*epcFilter = "3"
	*isoEquipmentCategoryIdentifier = "U"
	*isoDataIdeintifier = "J"
}

func decodeBitString(t *testing.T, bs string) []byte {
	t.Helper()
	hexStr, err := binutil.ParseBinStringToHexString(bs)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 0, len(hexStr)/2)
	for i := 0; i+1 < len(hexStr); i += 2 {
		var b byte
		for j := 0; j < 2; j++ {
			c := hexStr[i+j]
			switch {
			case c >= '0' && c <= '9':
				b = b<<4 | (c - '0')
			case c >= 'a' && c <= 'f':
				b = b<<4 | (c - 'a' + 10)
			default:
				t.Fatalf("unexpected hex character %q", c)
			}
		}
		out = append(out, b)
	}
	return out
}

// TestMakeEPCProducesDecodableTags checks generated tags translate back to the
// values they were generated from, for every supported scheme.
func TestMakeEPCProducesDecodableTags(t *testing.T) {
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	for _, scheme := range []string{"sgtin-96", "sscc-96", "giai-96", "grai-96"} {
		t.Run(scheme, func(t *testing.T) {
			reset()
			*epcScheme = scheme
			*epcCompanyPrefix = "0614141"
			bs, pc, err := makeEPC(false)
			if err != nil {
				t.Fatal(err)
			}
			if len(bs) != 96 {
				t.Fatalf("generated %d bits", len(bs))
			}
			if pc != "3000" {
				t.Errorf("protocol control = %s, want 3000", pc)
			}
			got, err := translator.Translate(tdt.Input{Bits: decodeBitString(t, bs), BitLen: 96})
			if err != nil {
				t.Fatalf("generated tag did not translate: %v", err)
			}
			if string(got.Encoding.Scheme) != scheme {
				t.Errorf("scheme = %s", got.Encoding.Scheme)
			}
			if got.Encoding.Control.FilterValue != 3 {
				t.Errorf("filter = %d", got.Encoding.Control.FilterValue)
			}
			gcp, ok := got.Identity.Component(identity.GS1CompanyPrefix)
			if !ok || gcp.Value != "0614141" {
				t.Errorf("company prefix = %q", gcp.Value)
			}
		})
	}
}

// TestMakeEPCPattern checks pattern mode emits a compilable pattern and the
// bit prefix a prefix matcher would use.
func TestMakeEPCPattern(t *testing.T) {
	reset()
	*epcScheme = "sgtin-96"
	*epcCompanyPrefix = "0614141"
	*epcItemReference = "812345"
	prefix, text, err := makeEPC(true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "urn:epc:pat:sgtin-96:3.0614141.812345.") || !strings.HasSuffix(text, ".*") {
		t.Fatalf("pattern = %s", text)
	}
	if _, err := tdt.CompilePattern(text); err != nil {
		t.Fatalf("generated pattern does not compile: %v", err)
	}
	// The serial is starred, so the prefix stops where the serial begins.
	if len(prefix) != 58 {
		t.Errorf("prefix covers %d bits, want 58", len(prefix))
	}
	if strings.Trim(prefix, "01") != "" {
		t.Errorf("prefix is not a bit string: %s", prefix)
	}
}

// TestMakeISORoundTrips checks generated transport-unit tags carry the right
// AFI and decode through the translation boundary.
func TestMakeISORoundTrips(t *testing.T) {
	reset()
	*isoIssuingAgencyCode = "UN"
	*isoCompanyIdentification = "043325711"
	*isoSerialNumber = "MH80"
	bs, pc, err := makeISO("iso17360-transport-unit", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs)%16 != 0 {
		t.Errorf("generated %d bits, not whole words", len(bs))
	}
	// Six 16-bit words of UII, the numbering-system toggle set, AFI 0xA2.
	if pc != "31a2" {
		t.Errorf("protocol control = %s, want 31a2", pc)
	}
	got, err := tdt.New().Translate(tdt.Input{Bits: decodeBitString(t, bs), BitLen: len(bs), PC: 0x31a2, HasPC: true})
	if err != nil {
		t.Fatalf("generated ISO tag did not translate: %v", err)
	}
	di, _ := got.Identity.Component(identity.DataIdentifier)
	if di.Value != "J" {
		t.Errorf("data identifier = %q", di.Value)
	}

	// A payload that would need padding is refused, not padded.
	reset()
	*isoIssuingAgencyCode = "UN"
	// "J" + "UN" + "0433" is seven characters, which does not fill a word.
	*isoCompanyIdentification = "0433"
	if _, _, err := makeISO("iso17360-transport-unit", false); !errors.Is(err, tdt.ErrUnresolvedSemantics) {
		t.Errorf("unaligned payload: error = %v, want ErrUnresolvedSemantics", err)
	}

	reset()
	if _, _, err := makeISO("iso17360-transport-unit", false); err == nil {
		t.Error("accepted a transport unit request with no identifier")
	}
}

// TestISO17363GenerationIsRefused checks that tagstrak does not write a tag
// whose carriage is not established.
func TestISO17363GenerationIsRefused(t *testing.T) {
	reset()
	*isoOwnerCode = "CSQ"
	*isoContainerSerialNumber = "305438"
	if _, _, err := makeISO("iso17363", false); err == nil {
		t.Error("wrote an ISO 17363 tag despite unresolved carriage")
	}
}

// TestUnsupportedSchemeIsRejected checks the allowlist is honoured here too.
func TestUnsupportedSchemeIsRejected(t *testing.T) {
	reset()
	*epcScheme = "sgtin-198"
	if _, _, err := makeEPC(false); err == nil {
		t.Error("generated an unsupported scheme")
	}
}
