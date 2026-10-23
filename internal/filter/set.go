package filter

import "sync/atomic"

// Set holds the current Matcher and replaces it atomically.
//
// Replacement compiles a whole new matcher before publishing it, so a
// concurrent Match observes either the previous subscriptions or the new ones,
// never a partial update. The zero value matches nothing and is ready to use.
type Set struct {
	current atomic.Pointer[snapshot]
}

// snapshot boxes a Matcher, because atomic.Pointer needs a concrete type.
type snapshot struct {
	matcher Matcher
}

var empty Matcher = &linear{}

// Matcher returns the current matcher. Callers that match several subjects
// against one consistent set should hold the result rather than call Match on
// the Set repeatedly.
func (s *Set) Matcher() Matcher {
	if snap := s.current.Load(); snap != nil {
		return snap.matcher
	}
	return empty
}

// Replace compiles subscriptions and publishes them. On error the current
// matcher is kept.
func (s *Set) Replace(subscriptions []Subscription) error {
	m, err := Compile(subscriptions)
	if err != nil {
		return err
	}
	s.current.Store(&snapshot{matcher: m})
	return nil
}

// Match matches a subject against the current matcher.
func (s *Set) Match(subject Subject, dst []ID) []ID {
	return s.Matcher().Match(subject, dst)
}
