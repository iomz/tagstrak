package llrp

import (
	"bytes"
	"errors"
	"testing"
)

func TestDecodeReadEvents(t *testing.T) {
	body, err := NewTagReportDataParam([]byte{0x30, 0x2d, 0xb3, 0x19, 0xa0, 0, 0, 0x40, 0, 0, 0, 3}, 0x3000)
	if err != nil {
		t.Fatal(err)
	}
	events, err := DecodeReadEvents(body, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !bytes.Equal(events[0].ID, []byte{0x30, 0x2d, 0xb3, 0x19, 0xa0, 0, 0, 0x40, 0, 0, 0, 3}) || !bytes.Equal(events[0].PC, []byte{0x30, 0}) {
		t.Fatalf("events = %#v", events)
	}
}

func TestDecodeReadEventsRejectsMalformedNestedParameters(t *testing.T) {
	malformed := []byte{0, 240, 0, 8, 0x8d, 0x30}
	if _, err := DecodeReadEvents(malformed, DefaultLimits()); !errors.Is(err, ErrParameterTruncated) {
		t.Fatalf("error = %v, want ErrParameterTruncated", err)
	}
}

func TestDecodeReadEventsNeverPanicsOnMalformedInput(t *testing.T) {
	for _, body := range [][]byte{
		nil,
		{0},
		{0, 240, 0, 4},
		{0, 240, 0, 5, 0x8d},
	} {
		_, _ = DecodeReadEvents(body, DefaultLimits())
	}
}

func FuzzDecodeReadEventsNeverPanics(f *testing.F) {
	f.Add([]byte{0, 240, 0, 4})
	f.Fuzz(func(t *testing.T, body []byte) {
		_, _ = DecodeReadEvents(body, DefaultLimits())
	})
}
