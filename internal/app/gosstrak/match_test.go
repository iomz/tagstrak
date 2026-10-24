package gosstrak

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/tdt"
)

// TestMatchingProcessorMatchesTranslations runs the whole processing path:
// an observation is translated, adapted to a filter subject, and matched.
func TestMatchingProcessorMatchesTranslations(t *testing.T) {
	subscriptions := table(t, nil,
		configfile.Subscription{ID: "product", Patterns: []string{"urn:epc:idpat:sgtin:0614141.812345.*"}},
		configfile.Subscription{ID: "pallets", Patterns: []string{"urn:epc:idpat:sscc:*.*"}},
	)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	processor, err := NewMatchingProcessor(subscriptions, logger)
	if err != nil {
		t.Fatal(err)
	}
	consumer := NewTranslatingConsumer(tdt.New(), processor)

	// SGTIN-96 3.0614141.812345.6789.
	if err := consumer.Consume(context.Background(), observation(t, "3074257bf7194e4000001a85", 0x3000, true)); err != nil {
		t.Fatal(err)
	}
	if got := logs.String(); !strings.Contains(got, "subscriptions=[product]") {
		t.Fatalf("log does not report the match: %s", got)
	}
}

func TestMatchingProcessorHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr, err := tdt.ParseTagURI("urn:epc:tag:sgtin-96:3.0614141.812345.6789")
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewMatchingProcessor(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Process(ctx, observation(t, "3074257bf7194e4000001a85", 0x3000, true), tr); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// TestSubjectOfViewsTranslation checks that the adapter carries every input
// matching needs, without copying the encoding.
func TestSubjectOfViewsTranslation(t *testing.T) {
	tr, err := tdt.ParseTagURI("urn:epc:tag:sgtin-96:3.0614141.812345.6789")
	if err != nil {
		t.Fatal(err)
	}
	s := SubjectOf(tr)
	if !s.Identity.Equal(tr.Identity) || s.Scheme != tr.Encoding.Scheme || s.BitLen != tr.Encoding.BitLen {
		t.Fatalf("subject %+v does not mirror the translation", s)
	}
	if &s.Bits[0] != &tr.Encoding.RawBits()[0] || &s.Fields[0] != &tr.Encoding.RawFields()[0] {
		t.Fatal("subject copies the encoding")
	}
	if n := testing.AllocsPerRun(100, func() { _ = SubjectOf(tr) }); n != 0 {
		t.Fatalf("SubjectOf allocates %v times", n)
	}
}
