package tdt_test

import (
	"fmt"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// Example_filteringContract shows the whole contract a matcher depends on.
//
// Subscriptions are compiled once, by this package, into immutable patterns.
// Tags are translated once into an identity bound to its encoding. Matching is
// then either a field comparison or, where the pattern publishes one, a
// value/mask bit test — and a matcher never parses identifier syntax, learns
// what a partition is, or sees the filter value unless the pattern asked for
// it.
func Example_filteringContract() {
	// Compiled once when a subscription is accepted.
	pattern, err := tdt.CompilePattern("urn:epc:idpat:sgtin:0614141.812345.*")
	if err != nil {
		panic(err)
	}

	translator := tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1))
	for _, filter := range []uint8{0, 3} {
		value, err := tdt.ParsePureIdentityURI("urn:epc:id:sgtin:0614141.812345.6789")
		if err != nil {
			panic(err)
		}
		written, err := tdt.EncodeGS1(value, "sgtin-96", filter)
		if err != nil {
			panic(err)
		}
		bits, bitLen := written.Encoding.Bits()

		read, err := translator.Translate(tdt.Input{Bits: bits, BitLen: bitLen})
		if err != nil {
			panic(err)
		}
		fmt.Printf("filter %d matches %v\n", read.Encoding.Control.FilterValue, read.Match(pattern))
	}

	// An encoding-level pattern does see the filter value, and publishes a bit
	// test a prefix matcher can use directly.
	encodingPattern, err := tdt.CompilePattern("urn:epc:pat:sgtin-96:3.0614141.812345.*")
	if err != nil {
		panic(err)
	}
	fmt.Println("encoding pattern level:", encodingPattern.Level())
	fmt.Println("bit test published:", encodingPattern.Bits() != nil)
	fmt.Println("identity pattern bit test published:", pattern.Bits() != nil)

	// Identities are compared structurally and key a map directly.
	counts := map[identity.Key]int{}
	value, _ := tdt.ParsePureIdentityURI("urn:epc:id:sgtin:0614141.812345.6789")
	counts[value.Key()]++
	counts[value.Key()]++
	fmt.Println("distinct identities:", len(counts))

	// Output:
	// filter 0 matches true
	// filter 3 matches true
	// encoding pattern level: encoding
	// bit test published: true
	// identity pattern bit test published: false
	// distinct identities: 1
}
