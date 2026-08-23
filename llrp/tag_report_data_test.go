package llrp

import "testing"

func TestNewTagReportDataParam(t *testing.T) {
	epc := []byte{0x30, 0x00}
	parameter := NewTagReportDataParam(epc, 0x1000)
	events, err := DecodeReadEvents(parameter, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || string(events[0].ID) != string(epc) {
		t.Fatalf("events = %#v", events)
	}
}
