// Copyright (c) 2018 Iori Mizutani
//
// Use of this source code is governed by The MIT License
// that can be found in the LICENSE file.

package llrp

import "errors"

var ErrEPCTooLong = errors.New("llrp: EPC exceeds 65535-bit limit")

// TagReportData holds an actual parameter in byte and
// how many tags are included in the parameter
type TagReportData struct {
	Data     []byte
	TagCount uint
}

// NewTagReportDataParam encodes one LLRP TagReportData parameter from wire values.
func NewTagReportDataParam(epc []byte, pcBits uint16) ([]byte, error) {
	if len(epc) > 8191 {
		return nil, ErrEPCTooLong
	}
	epcLengthBits := len(epc) * 8
	length := 4 + 2 + len(epc)
	epcd := EPCData(uint16(length), uint16(epcLengthBits), epc)

	// ChannlenIndex
	//chIndex := ChannelIndex()

	// LastSeenTimeStamp
	//timestamp := LastSeenTimestampUTC()

	// TagSeenCount
	//tagSeenCount := TagSeenCount()

	// AirProtocolTagData
	aptd := C1G2PC(pcBits)

	//tagReportDataLength := 4 + len(epcd) + len(chIndex) + len(timestamp) + len(tagSeenCount) // Rsvd+Type+length->32bits=4bytes
	tagReportDataLength := len(epcd) + len(aptd) + 4 // Rsvd+Type+length->32bits=4bytes

	// Pack in []byte
	return Pack([]interface{}{
		uint16(240),                 // Rsvd+Type=240 (TagReportData parameter)
		uint16(tagReportDataLength), // Length
		epcd,
		//chIndex,
		//timestamp,
		//tagSeenCount,
		aptd,
	}), nil
}
