// Package tdtcore holds the values and error taxonomy shared by the
// standard-specific translation packages and re-exported by internal/tdt.
//
// It owns the physical/encoding half of the domain — Encoding and Control —
// which is deliberately separate from identity.Value. Anything a standard uses
// to control how an identity is written onto a tag (EPC filter value,
// partition, tag length, Gen2 PC toggle, AFI, residue bits) belongs in Control
// and never in the canonical identity.
//
// Errors are classified so callers can act on them with errors.Is and
// errors.As without string matching: recognition failures, structural decode
// failures, and per-field validation failures are distinct kinds.
package tdtcore

import (
	"errors"
	"fmt"
)

// Recognition and support errors.
var (
	// ErrUnknownHeader means no supported encoding claims these bits.
	ErrUnknownHeader = errors.New("tdt: unrecognised encoding header")
	// ErrUnsupportedScheme means the encoding was recognised but is outside
	// the supported baseline. Upstream metadata existing for a scheme is not a
	// support commitment.
	ErrUnsupportedScheme = errors.New("tdt: unsupported scheme")
	// ErrUnrecognisedNonGS1 means a tag declared non-GS1 data whose AFI is not
	// mapped to a supported ISO data construct.
	ErrUnrecognisedNonGS1 = errors.New("tdt: unrecognised non-GS1 data format")
	// ErrAmbiguousEncoding means the input lacked the Gen2 PC word needed to
	// decide between GS1 and non-GS1 data, and no default was configured.
	ErrAmbiguousEncoding = errors.New("tdt: cannot distinguish GS1 from non-GS1 data without PC")
	// ErrUnresolvedSemantics means the data was recognised and decoded as far
	// as the published evidence allows, and going further would require
	// inventing wire semantics. See issue #8 Amendment 2 and the evidence
	// gates it carries forward.
	ErrUnresolvedSemantics = errors.New("tdt: encoding semantics are not established by the available standards evidence")
	// ErrXPCPresent means the Gen2 PC word has XI asserted, so the tag
	// backscatters one or two XPC words before the UII and the PC length field
	// counts them. Without the XPC words the UII extent cannot be established,
	// and the surplus is never consumed as UII data.
	ErrXPCPresent = errors.New("tdt: PC indicates XPC words precede the UII")
)

// Structural decode errors.
var (
	// ErrTruncated means the input is shorter than the encoding requires.
	ErrTruncated = errors.New("tdt: truncated encoding")
	// ErrReservedBits means fixed bits required by the encoding are wrong.
	ErrReservedBits = errors.New("tdt: reserved bits do not match the encoding")
	// ErrUnknownPartition means the partition value has no defined option.
	ErrUnknownPartition = errors.New("tdt: unknown partition")
	// ErrLengthMismatch means an observed bit length disagrees with the length
	// a Gen2 StoredPC declares for the UII. The surplus is never consumed.
	// The length field of a backscattered PacketPC counts any XPC words too;
	// that case is ErrXPCPresent, not this one.
	ErrLengthMismatch = errors.New("tdt: observed length disagrees with the PC length field")
	// ErrMalformedURI means an identifier URI does not match its grammar.
	ErrMalformedURI = errors.New("tdt: malformed identifier URI")
	// ErrMalformedPattern means a pattern string does not match its grammar.
	ErrMalformedPattern = errors.New("tdt: malformed pattern")
)

// Validation errors.
var (
	// ErrFieldValue is the class of all per-field validation failures.
	ErrFieldValue = errors.New("tdt: invalid field value")
	// ErrCheckDigit means a standard-defined check digit did not verify.
	ErrCheckDigit = errors.New("tdt: check digit mismatch")
	// ErrCannotEncode means a valid identity cannot be written in the
	// requested encoding. The translator never silently selects another one.
	ErrCannotEncode = errors.New("tdt: identity does not fit the requested encoding")
)

// FieldError names the field and the rule that rejected a value.
type FieldError struct {
	Scheme string
	Field  string
	// Rule is a short machine-stable reason, such as "range", "charset",
	// "length" or "leading-zero".
	Rule  string
	Value string
	// Spec cites the standard clause the rule comes from, where one applies.
	Spec string
	Err  error
}

// Error implements error.
func (e *FieldError) Error() string {
	msg := fmt.Sprintf("tdt: %s field %s: %s", e.Scheme, e.Field, e.Rule)
	if e.Value != "" {
		msg += fmt.Sprintf(" (%q)", e.Value)
	}
	if e.Spec != "" {
		msg += " [" + e.Spec + "]"
	}
	return msg
}

// Unwrap reports the error class, so errors.Is(err, ErrFieldValue) holds for
// every field validation failure.
func (e *FieldError) Unwrap() error {
	if e.Err != nil {
		return e.Err
	}
	return ErrFieldValue
}

// Field builds a FieldError in the ErrFieldValue class.
func Field(scheme, field, rule, value, spec string) *FieldError {
	return &FieldError{Scheme: scheme, Field: field, Rule: rule, Value: value, Spec: spec}
}
