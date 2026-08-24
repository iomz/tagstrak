package llrp

import (
	"errors"
	"testing"
)

func TestNewTagReportDataParam(t *testing.T) {
	epc := []byte{0x30, 0x00}
	parameter, err := NewTagReportDataParam(epc, 0x1000)
	if err != nil {
		t.Fatal(err)
	}
	events, err := DecodeReadEvents(parameter, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || string(events[0].ID) != string(epc) {
		t.Fatalf("events = %#v", events)
	}
}

func TestNewTagReportDataParamRejectsOversizedEPC(t *testing.T) {
	if _, err := NewTagReportDataParam(make([]byte, 8192), 0); !errors.Is(err, ErrEPCTooLong) {
		t.Fatalf("error = %v", err)
	}
}
