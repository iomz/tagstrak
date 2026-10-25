package tagstrak

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// Processor owns processing after translation. It receives canonical
// identities bound to their encodings, never raw tag bits, and never parses
// identifier syntax itself.
//
// Filtering will implement this interface; the contract is deliberately narrow
// so that it can.
type Processor interface {
	Process(context.Context, inventory.Observation, tdt.Translation) error
}

// TranslatingConsumer adapts ingested observations to the translation
// boundary and hands the result to a Processor.
//
// Adapting the application's observation type here is what keeps internal/tdt
// free of application dependencies: the translation packages take a narrow
// Input struct and know nothing about inventory, readers or reports.
type TranslatingConsumer struct {
	translator *tdt.Translator
	processor  Processor

	translated   atomic.Uint64
	untranslated atomic.Uint64
}

// NewTranslatingConsumer builds a consumer around a translator.
func NewTranslatingConsumer(translator *tdt.Translator, processor Processor) *TranslatingConsumer {
	if translator == nil {
		translator = tdt.New()
	}
	return &TranslatingConsumer{translator: translator, processor: processor}
}

// Consume translates one observation.
//
// A tag the supported standards do not cover is an observation about the
// world, not an application failure: it is counted and dropped. Anything else
// is returned, because it means this build cannot process data it claims to
// support.
func (c *TranslatingConsumer) Consume(ctx context.Context, observation inventory.Observation) error {
	input := TranslationInput(observation)
	translation, err := c.translator.Translate(input)
	if err != nil {
		if untranslatable(err) {
			c.untranslated.Add(1)
			return nil
		}
		return err
	}
	c.translated.Add(1)
	if c.processor == nil {
		return nil
	}
	return c.processor.Process(ctx, observation, translation)
}

// Counts reports how many observations translated and how many carried data
// outside the supported standards.
// AdaptiveStats forwards the processor's adaptive decisions, if it has any.
func (c *TranslatingConsumer) AdaptiveStats() (filter.AdaptiveStats, bool) {
	if source, ok := c.processor.(adaptiveStatsSource); ok {
		return source.AdaptiveStats()
	}
	return filter.AdaptiveStats{}, false
}

func (c *TranslatingConsumer) Counts() (translated, untranslatable uint64) {
	return c.translated.Load(), c.untranslated.Load()
}

// TranslationInput adapts an observation to the translation boundary.
func TranslationInput(observation inventory.Observation) tdt.Input {
	epc := observation.Tag().EPC()
	input := tdt.Input{Bits: epc, BitLen: len(epc) * 8}
	if pc, ok := observation.ProtocolControl(); ok {
		input.PC, input.HasPC = pc, true
	}
	return input
}

// untranslatable reports whether an error means "this tag is not something we
// claim to read", as opposed to a defect in this program.
//
// It includes ErrUnresolvedSemantics: a tag whose encoding semantics the
// published standards evidence does not establish is an observation about the
// world, and refusing it is the designed behaviour rather than a failure. It
// also includes ErrLengthMismatch and ErrXPCPresent, both of which are
// properties of what a reader reported rather than of this process: a tag that
// backscatters XPC words before its UII cannot have its UII extent resolved
// from the PC word alone, and this build does not read XPC.
func untranslatable(err error) bool {
	return errors.Is(err, tdt.ErrUnknownHeader) ||
		errors.Is(err, tdt.ErrUnsupportedScheme) ||
		errors.Is(err, tdt.ErrUnrecognisedNonGS1) ||
		errors.Is(err, tdt.ErrAmbiguousEncoding) ||
		errors.Is(err, tdt.ErrUnresolvedSemantics) ||
		errors.Is(err, tdt.ErrXPCPresent) ||
		errors.Is(err, tdt.ErrLengthMismatch) ||
		errors.Is(err, tdt.ErrUnknownPartition) ||
		errors.Is(err, tdt.ErrReservedBits) ||
		errors.Is(err, tdt.ErrFieldValue) ||
		errors.Is(err, tdt.ErrTruncated)
}
