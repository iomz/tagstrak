package filter

import "github.com/iomz/tagstrak/v2/internal/identity"

// linear tests every subscription in order. It is the single-mode default:
// simple enough to be obviously correct, which is what a control group for
// faster candidates needs to be.
type linear struct {
	subscriptions []compiled
}

type compiled struct {
	id       ID
	patterns []identity.Pattern
}

func compileLinear(subscriptions []Subscription) (*linear, error) {
	if err := validate(subscriptions); err != nil {
		return nil, err
	}
	m := &linear{subscriptions: make([]compiled, len(subscriptions))}
	for i, s := range subscriptions {
		m.subscriptions[i] = compiled{
			id:       s.ID,
			patterns: append([]identity.Pattern(nil), s.Patterns...),
		}
	}
	return m, nil
}

func (m *linear) Match(s Subject, dst []ID) []ID {
	for _, sub := range m.subscriptions {
		for _, p := range sub.patterns {
			if matches(p, s) {
				dst = append(dst, sub.id)
				break
			}
		}
	}
	return dst
}

func (m *linear) Len() int { return len(m.subscriptions) }
