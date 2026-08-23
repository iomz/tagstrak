package filtering

import "github.com/iomz/tagstrak/v2/llrp"

func benchmarkReadEvents(count int) []*llrp.ReadEvent {
	events := make([]*llrp.ReadEvent, count)
	for i := range events {
		events[i] = &llrp.ReadEvent{PC: []byte{0x30, 0x00}, ID: []byte{0x30, 0x00, byte(i >> 8), byte(i)}}
	}
	return events
}
