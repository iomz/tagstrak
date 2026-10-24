package llrp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// frameOfLength returns an ROAccessReport frame whose header claims length
// and whose body is padded with zeros to actual bytes in total.
func frameOfLength(length uint32, actual int) []byte {
	frame := make([]byte, max(actual, 10))
	binary.BigEndian.PutUint16(frame[0:2], ROAccessReportHeader)
	binary.BigEndian.PutUint32(frame[2:6], length)
	binary.BigEndian.PutUint32(frame[6:10], 1)
	return frame[:actual]
}

// tlv returns one TLV parameter whose length field is n.
func tlv(typ uint16, n int) []byte {
	p := make([]byte, max(n, 4))
	binary.BigEndian.PutUint16(p[0:2], typ)
	binary.BigEndian.PutUint16(p[2:4], uint16(n))
	return p
}

// addMessageBoundarySeeds adds frames at, over and lying about the frame limit.
func addMessageBoundarySeeds(f *testing.F) {
	limits := DefaultLimits()
	at := int(limits.MaxFrameSize)
	f.Add(frameOfLength(uint32(at), at))
	f.Add(frameOfLength(uint32(at+1), at+1))
	f.Add(frameOfLength(0xffffffff, 10))
	f.Add(frameOfLength(9, 10))
	f.Add(frameOfLength(10, 9))
}

// addParameterBoundarySeeds adds parameters at and over the size limit and
// parameter lists at and over the count limit.
func addParameterBoundarySeeds(f *testing.F) {
	limits := DefaultLimits()
	f.Add(tlv(240, int(limits.MaxParameterSize)))
	f.Add(tlv(240, int(limits.MaxParameterSize)-1)[:int(limits.MaxParameterSize)-1])
	f.Add(tlv(240, 0xffff)[:16])
	f.Add(bytes.Repeat(tlv(240, 4), limits.MaxParameters))
	f.Add(bytes.Repeat(tlv(240, 4), limits.MaxParameters+1))
}
