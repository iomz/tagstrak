package gosstrak

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// SubjectOf adapts a translation to the filtering boundary.
//
// Like TranslationInput on the way in, this keeps internal/filter free of
// translator types. The subject views the translation's fields and bits
// without copying them, so it must not outlive the translation.
func SubjectOf(translation tdt.Translation) filter.Subject {
	return filter.Subject{
		Identity: translation.Identity,
		Scheme:   translation.Encoding.Scheme,
		Fields:   translation.Encoding.RawFields(),
		Bits:     translation.Encoding.RawBits(),
		BitLen:   translation.Encoding.BitLen,
	}
}

// MatchingProcessor matches each translated observation against the current
// subscriptions, logs the result at debug level, and, when configured with
// DeliverTo, enqueues one report event per destination its matches route to.
type MatchingProcessor struct {
	subscriptions *SubscriptionTable
	logger        *slog.Logger

	delivery Delivery
	instance string
	sequence atomic.Uint64
}

// NewMatchingProcessor builds a processor around a subscription table. A nil
// table matches nothing, and a nil logger discards output.
func NewMatchingProcessor(subscriptions *SubscriptionTable, logger *slog.Logger, options ...ProcessorOption) (*MatchingProcessor, error) {
	if subscriptions == nil {
		subscriptions = NewSubscriptionTable(nil)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	p := &MatchingProcessor{subscriptions: subscriptions, logger: logger}
	for _, option := range options {
		if err := option(p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// AdaptiveStats reports the subscription table's adaptive decisions, if any.
func (p *MatchingProcessor) AdaptiveStats() (filter.AdaptiveStats, bool) {
	return p.subscriptions.AdaptiveStats()
}

// Process matches one translation and hands any routed matches to delivery.
// Enqueueing never blocks; a full destination queue drops and counts the
// event there, so a slow destination cannot stall ingestion.
func (p *MatchingProcessor) Process(ctx context.Context, observation inventory.Observation, translation tdt.Translation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// One snapshot serves the whole observation, so matching and routing
	// always see the same subscription revision.
	snapshot := p.subscriptions.current.Load()
	matched := snapshot.matcher.Match(SubjectOf(translation), nil)
	if p.logger.Enabled(ctx, slog.LevelDebug) {
		p.logger.DebugContext(ctx, "translated tag",
			"source", observation.Source(),
			"scheme", string(translation.Encoding.Scheme),
			"identity", translation.Identity.Describe(),
			"subscriptions", matched)
	}
	if p.delivery == nil || len(matched) == 0 {
		return nil
	}
	order, byDestination := route(snapshot.routes, matched)
	if len(order) == 0 {
		return nil
	}
	// One ID per observation, shared by its events across destinations.
	base := EventOf(fmt.Sprintf("%s:%d", p.instance, p.sequence.Add(1)), observation, translation)
	for _, name := range order {
		event := base
		event.Subscriptions = byDestination[name]
		p.delivery.Enqueue(name, event)
	}
	return nil
}
