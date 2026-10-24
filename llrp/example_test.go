package llrp_test

import (
	"bytes"
	"fmt"

	"github.com/iomz/tagstrak/v2/llrp"
)

// Example reads frames as a client of a reader would: acknowledging
// keepalives and decoding tag reports into read events.
func Example() {
	// What a reader might send: a keepalive, then one tag report.
	var wire bytes.Buffer
	limits := llrp.DefaultLimits()
	trd, _ := llrp.NewTagReportDataParam([]byte{0x30, 0x74, 0x25, 0x7b, 0xf7, 0x19, 0x4e, 0x40, 0x00, 0x00, 0x1a, 0x85}, 0x3000)
	_ = llrp.WriteMessage(&wire, llrp.KeepaliveMessage(1), limits)
	_ = llrp.WriteMessage(&wire, llrp.ROAccessReportMessage(trd, 2), limits)

	var replies bytes.Buffer
	for wire.Len() > 0 {
		m, err := llrp.ReadMessage(&wire, limits)
		if err != nil {
			fmt.Println(err)
			return
		}
		switch m.Header.Type {
		case llrp.KeepaliveHeader:
			_ = llrp.WriteMessage(&replies, llrp.KeepaliveAckMessage(m.Header.ID), limits)
			fmt.Println("keepalive", m.Header.ID)
		case llrp.ROAccessReportHeader:
			events, err := llrp.DecodeReadEvents(m.Payload, limits)
			if err != nil {
				fmt.Println(err)
				return
			}
			for _, e := range events {
				fmt.Printf("epc %x pc %x\n", e.ID, e.PC)
			}
		}
	}
	// Output:
	// keepalive 1
	// epc 3074257bf7194e4000001a85 pc 3000
}
