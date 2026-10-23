package iso

import (
	"fmt"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

// wordBits is the Gen2 word size a UII occupies a whole number of.
const wordBits = 16

// The 6-bit codes of the ISO/IEC 15434 structural control characters.
//
// These are the five positions where the ISO/IEC 15962 6-bit universal TEXT
// mapping is NOT code = ASCII & 0x3F. Each holds a control character in place
// of the printable character the plain mask would give.
//
// Register Annex A; reproduced as the normative Annex A of the RAIN Alliance
// specification "RAIN Free-Form Encoding Schemes" (2025-12-15). ISO/IEC
// 17360:2023 5.4.3 quotes two of them directly, in 6-bit code form: the data
// identifier separator GS (011110b) and the message terminator EOT (100001b).
// See issue #19, Canonical Study Record Part 1 section 4.1.
const (
	CodeGS  byte = 0b011110 // group separator, displaces '^'
	CodeRS  byte = 0b011111 // record separator, displaces '_'
	CodeEOT byte = 0b100001 // end of transmission, displaces '!'
	CodeFS  byte = 0b100011 // file separator, displaces '#'
	CodeUS  byte = 0b100100 // unit separator, displaces '$'
)

// controls maps each structural control code to its ISO/IEC 646 byte value
// and its name. Byte values are those of ISO/IEC 15434:2025, 4.2 and Annex A.
var controls = map[byte]struct {
	Byte byte
	Name string
}{
	CodeGS:  {0x1D, "GS"},
	CodeRS:  {0x1E, "RS"},
	CodeEOT: {0x04, "EOT"},
	CodeFS:  {0x1C, "FS"},
	CodeUS:  {0x1F, "US"},
}

// Control is one structural control character found in a decoded UII, with the
// character position it occupied.
type Control struct {
	// Code is the 6-bit code point.
	Code byte
	// Name is the control's ISO/IEC 15434 name: GS, RS, EOT, FS or US.
	Name string
	// Byte is the control's ISO/IEC 646 byte value.
	Byte byte
	// Index is the character position in the decoded stream, counting both
	// data characters and controls.
	Index int
}

// Text is the result of the character-decoding stage.
//
// Data characters and structural control characters are separate outputs,
// because they are different kinds: a control is part of the message
// structure, not part of any identifier. A control is never returned in
// Characters and so can never reach data identifier matching or an identity
// component (issue #8 Amendment 2, decision B3).
type Text struct {
	// Characters holds the decoded data characters, in order. Structural
	// control characters are not included; they are reported in Controls.
	Characters string
	// Controls holds every structural control character the stream contained,
	// in order. What their presence implies for the surrounding layout is
	// deliberately not decided here: the separator and terminator roles are
	// established, but the complete/incomplete-EOT layout rule is established
	// only for the AFI 0xAD binary bitstream, and extending it to the 6-bit
	// constructs is Derived (issue #8 Amendment 2, decision B2 tier 2; issue
	// #19 gates U1, U2, U7, U10).
	Controls []Control
	// ResidueBits counts the bits after the last complete 6-bit code. Whether
	// they carry a truncated terminator or nothing is not established.
	ResidueBits int
}

// accepted reports whether a character can be encoded as 6-bit data.
//
// The repertoire is ASCII 0x20 through 0x5F minus the five positions that hold
// structural control characters, so '!', '#', '$', '^' and '_' are not
// encodable. '@' is 6-bit code 000000 and is an ordinary data character.
func accepted(c byte) bool {
	if c < 0x20 || c > 0x5F {
		return false
	}
	_, isControl := controls[c&0x3F]
	return !isControl
}

// toCode maps a data character to its 6-bit code.
//
// Annex A gives code = ASCII & 0x3F for every data character: 100000 is space
// (0x20), 110000 is '0' (0x30), 000000 is '@' (0x40) and 000001 is 'A' (0x41).
// Callers must check accepted first; the five control positions have no data
// character and are not reachable through this function.
func toCode(c byte) byte { return c & 0x3F }

// fromCode maps a 6-bit code to its data character, reporting false for the
// five codes that hold a structural control character instead.
func fromCode(v byte) (byte, bool) {
	if _, isControl := controls[v&0x3F]; isControl {
		return 0, false
	}
	if v < 0x20 {
		return v + 0x40, true
	}
	return v, true
}

// DecodeText decodes the 6-bit character stream of a UII into data characters
// and structural control characters.
//
// Nothing is trimmed and no layout is inferred. Which characters, if any, a
// writer intended as padding is not established by the evidence we hold, so
// trailing bits too short to form a code are reported as residue rather than
// interpreted (issue #19 gates U1, U10).
func DecodeText(bits []byte, bitLen int) (Text, error) {
	if bitLen < 6 || len(bits)*8 < bitLen {
		return Text{}, fmt.Errorf("%w: %d bits of 6-bit text in %d bytes", tdtcore.ErrTruncated, bitLen, len(bits))
	}
	count := bitLen / 6
	var b strings.Builder
	b.Grow(count)
	var found []Control
	for i := 0; i < count; i++ {
		code := byte(bitsAt(bits, i*6, 6))
		if c, ok := fromCode(code); ok {
			b.WriteByte(c)
			continue
		}
		ctl := controls[code]
		found = append(found, Control{Code: code, Name: ctl.Name, Byte: ctl.Byte, Index: i})
	}
	return Text{Characters: b.String(), Controls: found, ResidueBits: bitLen - count*6}, nil
}

// EncodeText encodes data characters into a word-aligned 6-bit UII.
//
// Encoding succeeds only when the character count already fills a whole number
// of 16-bit words, which needs a multiple of eight characters.
//
// The refusal is deliberate and its reason is narrow. The separator and
// terminator roles ARE established: GS separates data identifiers and EOT
// terminates the message (ISO/IEC 17360:2023, 5.4.3). What is not established
// is how 6-bit codes pack into the most-significant-bit-first UII array, and
// what a writer must put in the bits left over after the last complete code.
// Writing an unaligned payload would require choosing both, so it is refused
// rather than invented (issue #8 Amendment 2, decision B2; issue #19 gates U1,
// U2, U7, U10).
func EncodeText(scheme, text string) ([]byte, int, error) {
	if text == "" {
		return nil, 0, tdtcore.Field(scheme, "uii", "empty", text, "")
	}
	for i := 0; i < len(text); i++ {
		if !accepted(text[i]) {
			return nil, 0, tdtcore.Field(scheme, "uii", "charset", text,
				"ISO/IEC 15961-2 register Annex A: ASCII 0x20-0x5F excluding the five structural control positions")
		}
	}
	bitLen := len(text) * 6
	if bitLen%wordBits != 0 {
		return nil, 0, fmt.Errorf("%w: %s needs %d more bits to reach %d-bit word alignment, and the 6-bit packing and residue rules are not established",
			tdtcore.ErrUnresolvedSemantics, scheme, wordBits-bitLen%wordBits, wordBits)
	}
	bits := make([]byte, bitLen/8)
	for i := 0; i < len(text); i++ {
		setBits(bits, i*6, 6, uint64(toCode(text[i])))
	}
	return bits, bitLen, nil
}

func bitsAt(data []byte, offset, n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		bit := offset + i
		v = v<<1 | uint64(data[bit/8]>>(7-uint(bit%8))&1)
	}
	return v
}

func setBits(data []byte, offset, n int, v uint64) {
	for i := 0; i < n; i++ {
		bit := offset + i
		if v>>(uint(n-1-i))&1 == 1 {
			data[bit/8] |= 1 << (7 - uint(bit%8))
		}
	}
}
