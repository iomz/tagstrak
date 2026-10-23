package gosstrak

import (
	"context"
	"io"
	"log/slog"

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
// subscriptions. Delivering matches is issue #9; until it exists, matches are
// logged at debug level.
type MatchingProcessor struct {
	subscriptions *filter.Set
	logger        *slog.Logger
}

// NewMatchingProcessor builds a processor around a subscription set. A nil set
// matches nothing, and a nil logger discards output.
func NewMatchingProcessor(subscriptions *filter.Set, logger *slog.Logger) *MatchingProcessor {
	if subscriptions == nil {
		subscriptions = &filter.Set{}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &MatchingProcessor{subscriptions: subscriptions, logger: logger}
}

// Process matches one translation.
func (p *MatchingProcessor) Process(ctx context.Context, observation inventory.Observation, translation tdt.Translation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	matched := p.subscriptions.Match(SubjectOf(translation), nil)
	if p.logger.Enabled(ctx, slog.LevelDebug) {
		p.logger.DebugContext(ctx, "translated tag",
			"source", observation.Source(),
			"scheme", string(translation.Encoding.Scheme),
			"identity", translation.Identity.Describe(),
			"subscriptions", matched)
	}
	return nil
}
