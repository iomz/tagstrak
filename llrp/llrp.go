// Copyright (c) 2018 Iori Mizutani
//
// Use of this source code is governed by The MIT License
// that can be found in the LICENSE file.

package llrp

import (
	"bytes"
	"encoding/binary"
)

// LLRP header values, the header is composed of 2 bytes where the first
// is set to 0x40 or 1024, so 1025 would be 0x0401
const (
	GetReaderCapabilityHeader         = 1025 // type 1
	GetReaderConfigHeader             = 1026 // type 2
	SetReaderConfigHeader             = 1027 // type 3
	GetReaderCapabilityResponseHeader = 1035 // type 11
	GetReaderConfigResponseHeader     = 1036 // type 12
	SetReaderConfigResponseHeader     = 1037 // type 13
	AddRospecHeader                   = 1044 // type 20
	DeleteRospecHeader                = 1045 // type 21
	EnableRospecHeader                = 1048 // type 24
	AddRospecResponseHeader           = 1054 // type 30
	DeleteRospecResponseHeader        = 1055 // type 31
	EnableRospecResponseHeader        = 1058 // type 34
	DeleteAccessSpecHeader            = 1065 // type 41
	DeleteAccessSpecResponseHeader    = 1075 // type 51
	ROAccessReportHeader              = 1085 // type 61
	KeepaliveHeader                   = 1086 // type 62
	ReaderEventNotificationHeader     = 1087 // type 63
	EventsAndReportsHeader            = 1088 // type 64
	KeepaliveAckHeader                = 1096 // type 72
	ImpinjEnableCustomMessageHeader   = 2047 // type 1023
)

const ImpinjEnableCutomMessageHeader = ImpinjEnableCustomMessageHeader

// Pack the data into (partial) LLRP packet payload.
// TODO: count the data size and return resulting length ?
func Pack(data []interface{}) []byte {
	buf := new(bytes.Buffer)
	for _, v := range data {
		err := binary.Write(buf, binary.BigEndian, v)
		if err != nil {
			panic(err)
		}
	}
	return buf.Bytes()
}

// ReadEvent is the struct to hold data on RFTags
type ReadEvent struct {
	ID []byte
	PC []byte
}

// UnmarshalROAccessReportBody extracts ReadEvent values from an RO_ACCESS_REPORT body.
func UnmarshalROAccessReportBody(roarBody []byte) ([]*ReadEvent, error) {
	return DecodeReadEvents(roarBody, DefaultLimits())
}
