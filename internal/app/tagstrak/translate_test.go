package tagstrak

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/tdt"
	"github.com/iomz/tagstrak/v2/internal/tdt/iso"
)

func observation(t *testing.T, epcHex string, pc uint16, hasPC bool) inventory.Observation {
	t.Helper()
	epc, err := hex.DecodeString(epcHex)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := inventory.NewTag(epc)
	if err != nil {
		t.Fatal(err)
	}
	o, err := inventory.NewObservation(tag, time.Unix(1, 0), "test")
	if err != nil {
		t.Fatal(err)
	}
	if hasPC {
		o = o.WithProtocolControl(pc)
	}
	return o
}

type recorder struct {
	seen []tdt.Translation
}

func (r *recorder) Process(_ context.Context, _ inventory.Observation, tr tdt.Translation) error {
	r.seen = append(r.seen, tr)
	return nil
}

// TestTranslatingConsumerAdaptsObservations checks the application boundary:
// observations become translation input, including the PC word.
func TestTranslatingConsumerAdaptsObservations(t *testing.T) {
	rec := &recorder{}
	consumer := NewTranslatingConsumer(tdt.New(), rec)
	if err := consumer.Consume(context.Background(), observation(t, "3074257bf7194e4000001a85", 0x3000, true)); err != nil {
		t.Fatal(err)
	}
	if len(rec.seen) != 1 {
		t.Fatalf("processor saw %d translations", len(rec.seen))
	}
	if got := rec.seen[0].Encoding.Scheme; got != "sgtin-96" {
		t.Errorf("scheme = %s", got)
	}
	translated, untranslatable := consumer.Counts()
	if translated != 1 || untranslatable != 0 {
		t.Errorf("counts = %d, %d", translated, untranslatable)
	}
}

// TestTranslatingConsumerDropsUnsupportedTags checks that a tag outside the
// supported standards is counted and dropped rather than failing the run.
func TestTranslatingConsumerDropsUnsupportedTags(t *testing.T) {
	rec := &recorder{}
	consumer := NewTranslatingConsumer(tdt.New(tdt.WithDefaultFormat(tdt.FormatGS1)), rec)
	// A GID-96 tag: a real EPC scheme, deliberately outside the baseline.
	if err := consumer.Consume(context.Background(), observation(t, "350000000000000000000000", 0, false)); err != nil {
		t.Fatalf("unsupported tag failed the run: %v", err)
	}
	if len(rec.seen) != 0 {
		t.Errorf("processor saw an unsupported tag")
	}
	if translated, untranslatable := consumer.Counts(); translated != 0 || untranslatable != 1 {
		t.Errorf("counts = %d, %d", translated, untranslatable)
	}
}

// TestTranslationInputCarriesProtocolControl checks the adapter does not drop
// the PC word, which is what makes ISO recognition possible at all.
func TestTranslationInputCarriesProtocolControl(t *testing.T) {
	in := TranslationInput(observation(t, "3074257bf7194e4000001a85", 0x31a9, true))
	if !in.HasPC || in.PC != 0x31a9 {
		t.Errorf("input PC = 0x%04X, hasPC = %v", in.PC, in.HasPC)
	}
	if in.BitLen != 96 {
		t.Errorf("bit length = %d", in.BitLen)
	}
	bare := TranslationInput(observation(t, "3074257bf7194e4000001a85", 0, false))
	if bare.HasPC {
		t.Errorf("input invented a PC word")
	}
}

// TestTranslatingConsumerDropsUnresolvedSemantics checks that a tag the
// standards evidence does not let us interpret is counted and dropped rather
// than failing the run. ISO 17363 is the current example.
func TestTranslatingConsumerDropsUnresolvedSemantics(t *testing.T) {
	bits, bitLen, err := iso.EncodeText("probe", "7BCSQU30")
	if err != nil {
		t.Fatal(err)
	}
	pc := uint16(bitLen/16)<<11 | 0x0100 | uint16(iso.AFI17363)
	tag, err := inventory.NewTag(bits)
	if err != nil {
		t.Fatal(err)
	}
	o, err := inventory.NewObservation(tag, time.Unix(1, 0), "test")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	consumer := NewTranslatingConsumer(tdt.New(), rec)
	if err := consumer.Consume(context.Background(), o.WithProtocolControl(pc)); err != nil {
		t.Fatalf("unresolved semantics failed the run: %v", err)
	}
	if len(rec.seen) != 0 {
		t.Error("processor saw a tag we cannot interpret")
	}
	if translated, untranslatable := consumer.Counts(); translated != 0 || untranslatable != 1 {
		t.Errorf("counts = %d, %d", translated, untranslatable)
	}
}
