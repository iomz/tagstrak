package gosstrak

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// ErrRevisionMismatch reports a replacement whose expected revision is not
// the current one.
var ErrRevisionMismatch = errors.New("gosstrak: subscription revision mismatch")

// FieldError locates a subscription error, such as "subscriptions[1].patterns[0]".
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

// SubscriptionTable holds the current subscriptions, their routes, the
// specification they were compiled from, and a revision, as one snapshot.
//
// Matching reads the snapshot without locking. Replacement compiles a whole
// new snapshot, lets the caller persist it, and only then publishes it, so a
// tag never matches new subscriptions with old routes, and a failed write
// leaves the published state unchanged.
type SubscriptionTable struct {
	destinations map[string]bool
	newMatcher   filter.Compiler
	mu           sync.Mutex // serialises replacements
	current      atomic.Pointer[subscriptionSnapshot]
}

type subscriptionSnapshot struct {
	matcher  filter.Matcher
	routes   Routes
	spec     []configfile.Subscription
	revision uint64
}

// NewSubscriptionTable returns an empty table at revision 0. Routes may name
// only the given destinations, which are fixed for the process lifetime.
func NewSubscriptionTable(destinations []string) *SubscriptionTable {
	return NewSubscriptionTableWith(destinations, filter.Compile)
}

// NewSubscriptionTableWith is NewSubscriptionTable with an explicit matcher,
// used for every compilation for the table's lifetime.
func NewSubscriptionTableWith(destinations []string, compile filter.Compiler) *SubscriptionTable {
	t := &SubscriptionTable{destinations: make(map[string]bool, len(destinations)), newMatcher: compile}
	for _, name := range destinations {
		t.destinations[name] = true
	}
	empty, _ := compile(nil)
	t.current.Store(&subscriptionSnapshot{matcher: empty, routes: Routes{}})
	return t
}

// AdaptiveStats reports the current matcher's decisions when it is adaptive.
func (t *SubscriptionTable) AdaptiveStats() (filter.AdaptiveStats, bool) {
	if a, ok := t.current.Load().matcher.(*filter.Adaptive); ok {
		return a.Stats(), true
	}
	return filter.AdaptiveStats{}, false
}

// Destinations returns the destination names routes may use.
func (t *SubscriptionTable) Destinations() []string {
	names := make([]string, 0, len(t.destinations))
	for name := range t.destinations {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Snapshot returns a copy of the current specification and its revision.
func (t *SubscriptionTable) Snapshot() ([]configfile.Subscription, uint64) {
	s := t.current.Load()
	return cloneSpec(s.spec), s.revision
}

// Revision returns the current revision.
func (t *SubscriptionTable) Revision() uint64 { return t.current.Load().revision }

// Load publishes spec at an explicit revision, for startup from a file or a
// state file.
func (t *SubscriptionTable) Load(spec []configfile.Subscription, revision uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	next, err := t.compile(spec, revision)
	if err != nil {
		return err
	}
	t.current.Store(next)
	return nil
}

// Replace compiles spec as the next revision. When expected is non-nil it
// must equal the current revision. commit, when non-nil, runs before
// publication and can veto it, which is how a state file is written before
// the change takes effect. It returns the published revision.
func (t *SubscriptionTable) Replace(spec []configfile.Subscription, expected *uint64, commit func(revision uint64, spec []configfile.Subscription) error) (uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	current := t.current.Load()
	if expected != nil && *expected != current.revision {
		return current.revision, fmt.Errorf("%w: expected %d, current %d", ErrRevisionMismatch, *expected, current.revision)
	}
	next, err := t.compile(spec, current.revision+1)
	if err != nil {
		return current.revision, err
	}
	if commit != nil {
		if err := commit(next.revision, cloneSpec(next.spec)); err != nil {
			return current.revision, err
		}
	}
	t.current.Store(next)
	return next.revision, nil
}

// compile validates and compiles a specification. Errors are FieldErrors
// where a field is to blame.
func (t *SubscriptionTable) compile(spec []configfile.Subscription, revision uint64) (*subscriptionSnapshot, error) {
	if len(spec) > filter.MaxSubscriptions {
		return nil, &FieldError{Field: "subscriptions", Err: fmt.Errorf("%d exceeds %d", len(spec), filter.MaxSubscriptions)}
	}
	subscriptions := make([]filter.Subscription, len(spec))
	routes := Routes{}
	for i, s := range spec {
		if s.ID == "" {
			return nil, &FieldError{Field: fmt.Sprintf("subscriptions[%d].id", i), Err: errors.New("is required")}
		}
		if len(s.Patterns) == 0 {
			return nil, &FieldError{Field: fmt.Sprintf("subscriptions[%d].patterns", i), Err: errors.New("is required")}
		}
		if len(s.Patterns) > filter.MaxPatternsPerSubscription {
			return nil, &FieldError{Field: fmt.Sprintf("subscriptions[%d].patterns", i), Err: fmt.Errorf("%d exceeds %d", len(s.Patterns), filter.MaxPatternsPerSubscription)}
		}
		patterns := make([]identity.Pattern, len(s.Patterns))
		for j, text := range s.Patterns {
			p, err := tdt.CompilePattern(text)
			if err != nil {
				return nil, &FieldError{Field: fmt.Sprintf("subscriptions[%d].patterns[%d]", i, j), Err: err}
			}
			patterns[j] = p
		}
		for j, name := range s.Destinations {
			if !t.destinations[name] {
				return nil, &FieldError{Field: fmt.Sprintf("subscriptions[%d].destinations[%d]", i, j), Err: fmt.Errorf("unknown destination %q", name)}
			}
		}
		subscriptions[i] = filter.Subscription{ID: filter.ID(s.ID), Patterns: patterns}
		if len(s.Destinations) > 0 {
			routes[filter.ID(s.ID)] = slices.Clone(s.Destinations)
		}
	}
	matcher, err := t.newMatcher(subscriptions)
	if err != nil {
		return nil, &FieldError{Field: "subscriptions", Err: err}
	}
	return &subscriptionSnapshot{matcher: matcher, routes: routes, spec: cloneSpec(spec), revision: revision}, nil
}

func cloneSpec(spec []configfile.Subscription) []configfile.Subscription {
	out := make([]configfile.Subscription, len(spec))
	for i, s := range spec {
		out[i] = configfile.Subscription{ID: s.ID, Patterns: slices.Clone(s.Patterns), Destinations: slices.Clone(s.Destinations)}
	}
	return out
}
