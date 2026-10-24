package gosstrak

import (
	"context"
	"fmt"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/report"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// Routes maps subscription IDs to the names of the destinations their
// matches are delivered to. Routing lives here rather than in filter, which
// knows subscriptions, or report, which knows destinations.
type Routes map[filter.ID][]string

// Delivery is the asynchronous report path. *report.Dispatcher implements it.
//
// The App owns its lifecycle: Start before ingestion, Close after the
// consumer drains and within the shared shutdown deadline. The processor only
// enqueues, which never blocks.
type Delivery interface {
	Destinations() []string
	Start()
	Enqueue(destination string, event report.Event) bool
	Close(context.Context) error
	Stats() []report.Stats
}

// ProcessorOption configures a MatchingProcessor.
type ProcessorOption func(*MatchingProcessor) error

// DeliverTo routes matches to delivery. Every destination named by routes must
// exist in delivery.
func DeliverTo(delivery Delivery, routes Routes) ProcessorOption {
	return func(p *MatchingProcessor) error {
		if delivery == nil {
			return fmt.Errorf("gosstrak: delivery is required for routes")
		}
		known := make(map[string]bool)
		for _, name := range delivery.Destinations() {
			known[name] = true
		}
		copied := make(Routes, len(routes))
		for id, destinations := range routes {
			for _, name := range destinations {
				if !known[name] {
					return fmt.Errorf("gosstrak: subscription %q routes to unknown destination %q", id, name)
				}
			}
			copied[id] = append([]string(nil), destinations...)
		}
		p.delivery, p.routes = delivery, copied
		p.instance = report.NewInstanceID()
		return nil
	}
}

// route groups matched subscription IDs by destination. Destinations appear in
// the order their first matching subscription does, and each destination's
// subscriptions keep match order, so delivery is deterministic.
func (p *MatchingProcessor) route(matched []filter.ID) (order []string, byDestination map[string][]string) {
	for _, id := range matched {
		for _, name := range p.routes[id] {
			if byDestination == nil {
				byDestination = make(map[string][]string)
			}
			if _, seen := byDestination[name]; !seen {
				order = append(order, name)
			}
			byDestination[name] = append(byDestination[name], string(id))
		}
	}
	return order, byDestination
}

// EventOf renders one observation and its translation as a report event.
//
// It is the application boundary for reporting, as TranslationInput and
// SubjectOf are for translation and matching: report receives plain values
// and never imports identity or translator types. ISO identities have no
// standards-backed textual form, so their PureIdentityURI stays empty and
// their components carry the identity.
func EventOf(id string, observation inventory.Observation, translation tdt.Translation) report.Event {
	v := translation.Identity
	components := make(map[string]string, v.Len())
	for c := range v.Components() {
		components[c.Name.String()] = c.Value
	}
	uri, err := tdt.PureIdentityURI(v)
	if err != nil {
		uri = ""
	}
	return report.Event{
		ID:         id,
		Source:     observation.Source(),
		ObservedAt: observation.SeenAt(),
		Scheme:     string(translation.Encoding.Scheme),
		EPC:        observation.Tag().EPC(), // a copy; the queue may hold it
		Identity: report.Identity{
			Authority:  v.Authority().String(),
			KeyType:    v.KeyType().String(),
			Components: components,
		},
		PureIdentityURI: uri,
	}
}
