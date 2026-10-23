// Package iso implements the ISO non-GS1 tag data constructs tagstrak
// supports, to the extent the public standards evidence establishes them.
//
// # Stages
//
// Translation is deliberately three separate stages, because each fails for a
// different reason and the evidence behind each differs:
//
//	Recognise  AFI -> registered data construct        (ISO/IEC 15961-2 register)
//	DecodeText bits -> characters                      (register Annex A)
//	Interpret  characters -> canonical identity        (application standard)
//
// A failure in interpretation does not invalidate recognition or decoding, and
// an application whose payload rules are not established is refused at the
// third stage rather than guessed at.
//
// # Evidence
//
// AFI assignments are transcribed in registry.json from the ISO/IEC 15961-2
// Applications Family Identifier Assignments Register published by the
// Registration Authority (AIM Global), 2025-11-25 edition. The per-AFI data
// identifier sets come from the Data Constructs Register table of the
// SUPERSEDED 2023-09-25 edition: the current edition removed that table, so
// the sets are historical and serve as a recognition table, never as a
// validation rule. The 6-bit character set is the current register's Annex A,
// "The ISO/IEC 15962 6-bit universal TEXT character set for AIDC use".
//
// The Gen2 PC word carries the numbering-system toggle in bit 17h and the AFI
// in bits 18h-1Fh; the toggle selects GS1 EPC data when clear and non-GS1 data
// when set. See GS1 EPC Gen2 Release 3.0.1, section 6.3.2.1.2.2, which also
// defines the AFI field and delegates non-GS1 UII content to ISO/IEC 15962.
//
// Numbering authority is a property of the data identifier, never of the AFI:
// the "J" licence plate series and 25S, 25T, 25B and 5R qualify ISO/IEC 15459
// identities, while 1B, I, 7B and 7J do not.
//
// # Limitations
//
// These follow issue #8 Amendment 2, which reconciles this package against the
// standards evidence baseline in issue #19. They are not worked around:
//
//   - The separator and terminator ARE established: GS separates data
//     identifiers and EOT terminates the message (ISO/IEC 17360:2023, 5.4.3).
//     The layout they imply for a 6-bit construct is NOT. The
//     complete/incomplete-EOT rule is normative only for the AFI 0xAD binary
//     bitstream, so extending it here is Derived, as are the 6-bit packing
//     order and the meaning of residue bits (issue #19 gates U1, U2, U7, U10).
//     Nothing is trimmed on decode, controls and residue are surfaced rather
//     than acted on, and encoding refuses any payload that would need a layout
//     decision.
//   - ISO 17363 has no registered data construct. AFI 0xA9 and 0xAA are
//     reserved for it, but the register assigns them no compaction method,
//     ISO/IEC 15962 covers only ISO 17364, 17365, 17366 and 17367 in its data
//     identifier direct encoding clauses and annexes, and ISO 17363's own air
//     interface is 433 MHz or 2,45 GHz rather than ISO/IEC 18000-63. Such tags
//     are recognised; they are not decoded through Decode and not promoted
//     (issue #19 gate U3). InterpretContainerCode remains available for a
//     caller who knows their own tag layout.
//   - Decoding dispatches on the registered compaction method. The 6-bit codec
//     is never a fallback, so an AFI whose compaction this build does not
//     implement fails explicitly rather than being misread.
//   - Object identifiers are carried as provenance and never emitted. Where
//     two authoritative sources disagree, as they do for the data identifier
//     4J, both values are recorded and neither is adjudicated (issue #19
//     gate U5).
//
// ISO identities never use the urn:epc: namespace and get no invented
// canonical text form; the canonical identity is the typed identity.Value.
package iso

import (
	"fmt"

	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt/tdtcore"
)

//go:generate go run ./cmd/genafi -root ../../..

// Scheme labels for the data constructs tagstrak recognises.
//
// ISO/IEC 17360:2023 consolidated ISO 17364, 17365, 17366 and 17367, and the
// register labels AFI 0xA2 and 0xA7 "ISO/IEC 17360 transport unit". ISO 17363
// was not consolidated and keeps its own reservation.
const (
	SchemeTransportUnit identity.SchemeID = "iso17360-transport-unit"
	Scheme17363         identity.SchemeID = "iso17363"
)

// AFI assignments this package acts on. Every assigned value is in
// registry.json; these are the ones with a supported scheme.
const (
	AFITransportUnit          uint8 = 0xA2
	AFITransportUnitHazardous uint8 = 0xA7
	AFI17363                  uint8 = 0xA9
	AFI17363Historical        uint8 = 0xAA
)

// Source identifies the register edition the generated constructs come from.
type Source struct {
	Document           string
	Authority          string
	AssignmentsEdition string
	AssignmentsURL     string
	ConstructsEdition  string
	ConstructsURL      string
}

// DataIdentifier is one recognised ANS MH10.8.2 data identifier and the object
// identifier the superseded register edition assigned to the UII that carries
// it.
//
// Object identifiers are carried as provenance and are never emitted. Where
// two authoritative sources disagree, both values are recorded and neither is
// adjudicated (issue #8 Amendment 2, gate G5).
type DataIdentifier struct {
	DI  string
	OID string
	// OIDConflict is a competing value from another authoritative source,
	// empty when the sources agree.
	OIDConflict string
	// OIDNote explains a recorded conflict.
	OIDNote string
}

// Compaction names a Monomorphic-UII compaction method as the register
// records it. Decoding dispatches on it and never falls back to a default,
// so an AFI whose compaction this build does not implement fails explicitly
// rather than being misread (issue #8 Amendment 2, decision B9).
type Compaction string

// Compaction methods the register records for the AFIs in this table.
const (
	CompactionNone      Compaction = ""
	Compaction6Bit      Compaction = "6-bit"
	CompactionUTF8      Compaction = "utf-8"
	CompactionBinary    Compaction = "binary"
	CompactionURNCode40 Compaction = "URN Code 40"
	CompactionOctet8Bit Compaction = "application-defined 8-bit"
)

// Construct is the data construct registered to one AFI.
type Construct struct {
	AFI  uint8
	Role string
	// Encoding is the register's compaction column, empty where the register
	// assigns none.
	Encoding Compaction
	// Historical marks assignments the register says not to use in new
	// applications. They still decode.
	Historical bool
	// Supported marks the constructs this build acts on.
	Supported bool
	// DataIdentifiers is the set the superseded register edition assigned,
	// empty where none was assigned. It is a RECOGNITION table, never a
	// validation rule: the current edition removed the table and ISO/IEC
	// 17360:2023 5.2 permits any applicable ISO/IEC 15418 data identifier
	// (issue #8 Amendment 2, decision B6).
	DataIdentifiers []DataIdentifier
}

// Scheme returns the scheme label for a supported construct.
//
// This is deliberately a partial function. An AFI does not in general
// determine a supply chain layer: ISO/IEC 17360:2023 5.4.1 NOTE 2 makes the
// 8-bit AFI take precedence over and replace the licence plate and returnable
// transport item AFIs, and the binary AFI carries the layer in a UII-Type
// field instead. An AFI that names no layer is reported as such rather than
// being assigned one (issue #8 Amendment 2, decision B10).
func (c Construct) Scheme() (identity.SchemeID, error) {
	switch c.AFI {
	case AFITransportUnit, AFITransportUnitHazardous:
		return SchemeTransportUnit, nil
	case AFI17363, AFI17363Historical:
		return Scheme17363, nil
	default:
		return "", fmt.Errorf("%w: AFI 0x%02X does not determine a supply chain layer in this build", tdtcore.ErrUnsupportedScheme, c.AFI)
	}
}

// Allows reports whether a data identifier is in this construct's recognised
// set. A data identifier outside it is unrecognised, not invalid.
func (c Construct) Allows(di string) bool {
	for _, d := range c.DataIdentifiers {
		if d.DI == di {
			return true
		}
	}
	return false
}

// Constructs returns every registered data construct.
func Constructs() []Construct { return append([]Construct(nil), constructs...) }

// Recognise resolves an AFI to its registered data construct.
//
// An AFI with no register entry is not guessed at; an AFI that is registered
// but outside this build's baseline is reported as unsupported, which is a
// different condition and a different error.
func Recognise(afi uint8) (Construct, error) {
	for _, c := range constructs {
		if c.AFI != afi {
			continue
		}
		if !c.Supported {
			return c, fmt.Errorf("%w: AFI 0x%02X is registered to %q", tdtcore.ErrUnsupportedScheme, afi, c.Role)
		}
		return c, nil
	}
	return Construct{}, fmt.Errorf("%w: AFI 0x%02X", tdtcore.ErrUnrecognisedNonGS1, afi)
}

// SchemeForAFI returns the supported scheme registered to an AFI.
func SchemeForAFI(afi uint8) (identity.SchemeID, error) {
	c, err := Recognise(afi)
	if err != nil {
		return "", err
	}
	return c.Scheme()
}

// AFIForScheme returns the current, non-historical AFI for a scheme.
func AFIForScheme(s identity.SchemeID) (uint8, error) {
	switch s {
	case SchemeTransportUnit:
		return AFITransportUnit, nil
	case Scheme17363:
		return AFI17363, nil
	default:
		return 0, fmt.Errorf("%w: %s", tdtcore.ErrUnsupportedScheme, s)
	}
}
