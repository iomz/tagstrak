package filtering

import "github.com/iomz/tagstrak/v2/llrp"

func benchmarkReadEvents(count int) []*llrp.ReadEvent {
	events := make([]*llrp.ReadEvent, count)
	for i := range events {
		id := make([]byte, 12)
		id[0] = 0x30 // SGTIN-96
		id[1] = 0x6c // filter 3, partition 3
		id[8] = byte(i >> 24)
		id[9] = byte(i >> 16)
		id[10] = byte(i >> 8)
		id[11] = byte(i)
		events[i] = &llrp.ReadEvent{PC: []byte{0x30, 0x00}, ID: id}
	}
	return events
}
