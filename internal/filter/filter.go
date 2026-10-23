// Package filter matches translated tags against compiled subscriptions.
//
// It consumes the pattern contract published by internal/tdt: subscriptions
// carry immutable identity.Pattern values compiled by tdt.CompilePattern, and
// tags arrive as a Subject built from a translation at the application
// boundary. The package never parses pattern text and imports nothing but
// internal/identity, so matching semantics cannot drift from the translator's.
//
// # Contract
//
// Compile validates a subscription list and returns an immutable Matcher. A
// Set holds the current Matcher and replaces it whole, so a reader sees either
// the old subscriptions or the new ones, never a mix. Match appends matching
// subscription IDs, in subscription order and each at most once, to a buffer
// the caller owns. A subscription matches when any of its patterns matches.
//
// # Matchers
//
// The linear matcher is the single-mode default and the control group for
// later candidates (issue #7). Every matcher must agree with
// tdtcore.Translation.Match on the conformance corpus in
// internal/tdt/testdata/vectors/patterns.json.
package filter

import (
	"errors"
	"fmt"

	"github.com/iomz/tagstrak/v2/internal/identity"
)

// MaxSubscriptions bounds one compiled subscription set. Linear matching is
// O(subscriptions) per tag, so the budget also bounds per-tag work.
const MaxSubscriptions = 10000

// Errors returned by Compile.
var (
	ErrEmptyID              = errors.New("filter: empty subscription ID")
	ErrDuplicateID          = errors.New("filter: duplicate subscription ID")
	ErrNoPatterns           = errors.New("filter: subscription has no patterns")
	ErrZeroPattern          = errors.New("filter: subscription has an unset pattern")
	ErrTooManySubscriptions = errors.New("filter: too many subscriptions")
)

// ID names a subscription. It is chosen by whoever configures subscriptions
// and is opaque to matching.
type ID string

// Subscription is a named set of alternative patterns.
type Subscription struct {
	ID       ID
	Patterns []identity.Pattern
}

// Subject is one translated tag, as matching sees it.
//
// Fields and Bits are read-only views into the translation that produced
// them; a Subject must not outlive or modify that translation. They are views
// rather than copies so that building a Subject per tag does not allocate.
type Subject struct {
	// Identity is the canonical identity, matched by identity-level patterns.
	Identity identity.Value
	// Scheme is the encoding scheme, such as "sgtin-96".
	Scheme identity.SchemeID
	// Fields is the encoding-level component vector, including control
	// fields such as the EPC filter value.
	Fields []identity.Component
	// Bits holds the encoded tag bits; BitLen of them are significant.
	Bits   []byte
	BitLen int
}

// Matcher reports which subscriptions a subject matches. Implementations are
// immutable and safe for concurrent use.
type Matcher interface {
	// Match appends the IDs of matching subscriptions to dst, in
	// subscription order, and returns the extended slice.
	Match(s Subject, dst []ID) []ID
	// Len reports how many subscriptions the matcher holds.
	Len() int
}

// Compile validates subscriptions and builds the default matcher.
//
// The subscriptions are copied, so later changes to the argument do not reach
// the matcher.
func Compile(subscriptions []Subscription) (Matcher, error) {
	m, err := compileLinear(subscriptions)
	if err != nil {
		// Return a nil interface, not a nil *linear inside one.
		return nil, err
	}
	return m, nil
}

// validate checks a subscription list against the contract shared by every
// matcher implementation.
func validate(subscriptions []Subscription) error {
	if len(subscriptions) > MaxSubscriptions {
		return fmt.Errorf("%w: %d exceeds %d", ErrTooManySubscriptions, len(subscriptions), MaxSubscriptions)
	}
	seen := make(map[ID]struct{}, len(subscriptions))
	for i, s := range subscriptions {
		if s.ID == "" {
			return fmt.Errorf("%w at index %d", ErrEmptyID, i)
		}
		if _, ok := seen[s.ID]; ok {
			return fmt.Errorf("%w: %q", ErrDuplicateID, s.ID)
		}
		seen[s.ID] = struct{}{}
		if len(s.Patterns) == 0 {
			return fmt.Errorf("%w: %q", ErrNoPatterns, s.ID)
		}
		for _, p := range s.Patterns {
			if p.IsZero() {
				return fmt.Errorf("%w: %q", ErrZeroPattern, s.ID)
			}
		}
	}
	return nil
}

// matches is the reference rule every matcher implements: identity-level
// patterns see only the identity, and encoding-level patterns see only the
// encoding of their own scheme.
//
// Where a pattern publishes a bit test it is equivalent to field matching for
// translated tags, so the cheaper test is used.
func matches(p identity.Pattern, s Subject) bool {
	switch p.Level() {
	case identity.LevelIdentity:
		return p.MatchValue(s.Identity)
	case identity.LevelEncoding:
		if p.Scheme() != s.Scheme {
			return false
		}
		if bits := p.Bits(); bits != nil {
			return bits.Match(s.Bits, s.BitLen)
		}
		return p.MatchComponents(s.Fields)
	default:
		return false
	}
}
