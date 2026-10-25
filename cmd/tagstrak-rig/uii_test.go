package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
	"github.com/iomz/tagstrak/v2/internal/tdt/iso"
)

// TestMakeEPCProducesDecodableTags checks generated tags translate back to the
// values they were generated from, for every supported scheme.
func TestMakeEPCProducesDecodableTags(t *testing.T) {
	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	for _, scheme := range []string{"sgtin-96", "sscc-96", "giai-96", "grai-96"} {
		t.Run(scheme, func(t *testing.T) {
			got, err := makeEPC(uiiOptions{scheme: scheme, filter: "3", companyPrefix: "0614141"})
			if err != nil {
				t.Fatal(err)
			}
			if got.bitLen != 96 {
				t.Fatalf("generated %d bits", got.bitLen)
			}
			if got.pc != 0x3000 {
				t.Errorf("protocol control = %04x, want 3000", got.pc)
			}
			translated, err := translator.Translate(tdt.Input{Bits: got.bits, BitLen: 96})
			if err != nil {
				t.Fatalf("generated tag did not translate: %v", err)
			}
			if string(translated.Encoding.Scheme) != scheme {
				t.Errorf("scheme = %s", translated.Encoding.Scheme)
			}
			if translated.Encoding.Control.FilterValue != 3 {
				t.Errorf("filter = %d", translated.Encoding.Control.FilterValue)
			}
			gcp, ok := translated.Identity.Component(identity.GS1CompanyPrefix)
			if !ok || gcp.Value != "0614141" {
				t.Errorf("company prefix = %q", gcp.Value)
			}
		})
	}
}

// TestMakeEPCPattern checks pattern mode emits a compilable pattern and the
// bit prefix a prefix matcher would use.
func TestMakeEPCPattern(t *testing.T) {
	got, err := makeEPC(uiiOptions{pattern: true, scheme: "sgtin-96", filter: "3", companyPrefix: "0614141", itemRef: "812345"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.pattern, "urn:epc:pat:sgtin-96:3.0614141.812345.") || !strings.HasSuffix(got.pattern, ".*") {
		t.Fatalf("pattern = %s", got.pattern)
	}
	if _, err := tdt.CompilePattern(got.pattern); err != nil {
		t.Fatalf("generated pattern does not compile: %v", err)
	}
	// The serial is starred, so the prefix stops where the serial begins.
	if got.bitLen != 58 {
		t.Errorf("prefix covers %d bits, want 58", got.bitLen)
	}
}

// TestMakeISORoundTrips checks generated transport-unit tags carry the right
// AFI and decode through the translation boundary.
func TestMakeISORoundTrips(t *testing.T) {
	options := uiiOptions{dataIdentifier: "J", agency: "UN", company: "043325711", isoSerial: "MH80"}
	got, err := makeISO(options, iso.SchemeTransportUnit)
	if err != nil {
		t.Fatal(err)
	}
	if got.bitLen%16 != 0 {
		t.Errorf("generated %d bits, not whole words", got.bitLen)
	}
	// Six 16-bit words of UII, the numbering-system toggle set, AFI 0xA2.
	if got.pc != 0x31a2 {
		t.Errorf("protocol control = %04x, want 31a2", got.pc)
	}
	translated, err := tdt.New().Translate(tdt.Input{Bits: got.bits, BitLen: got.bitLen, PC: got.pc, HasPC: true})
	if err != nil {
		t.Fatalf("generated ISO tag did not translate: %v", err)
	}
	di, _ := translated.Identity.Component(identity.DataIdentifier)
	if di.Value != "J" {
		t.Errorf("data identifier = %q", di.Value)
	}

	// A payload that would need padding is refused, not padded.
	// "J" + "UN" + "0433" is seven characters, which does not fill a word.
	options = uiiOptions{dataIdentifier: "J", agency: "UN", company: "0433"}
	if _, err := makeISO(options, iso.SchemeTransportUnit); !errors.Is(err, tdt.ErrUnresolvedSemantics) {
		t.Errorf("unaligned payload: error = %v, want ErrUnresolvedSemantics", err)
	}

	if _, err := makeISO(uiiOptions{dataIdentifier: "J"}, iso.SchemeTransportUnit); err == nil {
		t.Error("accepted a transport unit request with no issuing agency")
	}
}

// TestISO17363GenerationIsRefused checks that tagstrak does not write a tag
// whose carriage is not established.
func TestISO17363GenerationIsRefused(t *testing.T) {
	if _, err := makeISO(uiiOptions{owner: "CSQ", category: "U", containerSerial: "305438"}, iso.Scheme17363); err == nil {
		t.Error("wrote an ISO 17363 tag despite unresolved carriage")
	}
}

// TestUnsupportedSchemeIsRejected checks the allowlist is honoured here too.
func TestUnsupportedSchemeIsRejected(t *testing.T) {
	if _, err := makeEPC(uiiOptions{scheme: "sgtin-198", filter: "3"}); err == nil {
		t.Error("generated an unsupported scheme")
	}
}

// TestUIICommandLine checks the subcommand's output formats and argument
// errors through run.
func TestUIICommandLine(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"uii", "epc", "--scheme", "sgtin-96", "--company-prefix", "0614141", "--item-ref", "812345", "--serial", "6789"},
			"3000,3074257bf7194e4000001a85\n"},
		{[]string{"uii", "epc", "--scheme", "sgtin-96", "--company-prefix", "0614141", "--item-ref", "812345", "--serial", "1", "--format", "bin"},
			"3000,001100000111010000100101011110111111011100011001010011100100000000000000000000000000000000000001\n"},
		{[]string{"uii", "epc", "--scheme", "sscc-96", "--company-prefix", "0614141", "--pattern"},
			"urn:epc:pat:sscc-96:3.0614141.*,00110001011101000010010101111011111101\n"},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), tc.args, &out, io.Discard); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if out.String() != tc.want {
			t.Errorf("%v: output = %q, want %q", tc.args, out.String(), tc.want)
		}
	}
	for _, args := range [][]string{
		{"uii"},
		{"uii", "upc"},
		{"uii", "epc"},
		{"uii", "epc", "--scheme", "sgtin-96", "--format", "dec"},
		{"uii", "epc", "--scheme", "sgtin-96", "extra"},
	} {
		if err := run(context.Background(), args, io.Discard, io.Discard); err == nil {
			t.Errorf("%v: accepted", args)
		} else if len(args) == 1 && !errors.Is(err, flag.ErrHelp) {
			t.Errorf("%v: error = %v, want help", args, err)
		}
	}
}
