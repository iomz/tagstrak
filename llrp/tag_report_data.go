// Copyright (c) 2018 Iori Mizutani
//
// Use of this source code is governed by The MIT License
// that can be found in the LICENSE file.

package llrp

import "errors"

var ErrEPCTooLong = errors.New("llrp: EPC exceeds 65535-bit limit")

// NewTagReportDataParam encodes one LLRP TagReportData parameter from wire values.
func NewTagReportDataParam(epc []byte, pcBits uint16) ([]byte, error) {
	if len(epc) > 8191 {
		return nil, ErrEPCTooLong
	}
	epcLengthBits := len(epc) * 8
	length := 4 + 2 + len(epc)
	epcd := EPCData(uint16(length), uint16(epcLengthBits), epc)

	// ChannlenIndex
	//chIndex := channelIndex()

	// LastSeenTimeStamp
	//timestamp := lastSeenTimestampUTC()

	// tagSeenCount
	//tagSeenCount := tagSeenCount()

	// AirProtocolTagData
	aptd := c1G2PC(pcBits)

	//tagReportDataLength := 4 + len(epcd) + len(chIndex) + len(timestamp) + len(tagSeenCount) // Rsvd+Type+length->32bits=4bytes
	tagReportDataLength := len(epcd) + len(aptd) + 4 // Rsvd+Type+length->32bits=4bytes

	// pack in []byte
	return pack([]interface{}{
		uint16(240),                 // Rsvd+Type=240 (TagReportData parameter)
		uint16(tagReportDataLength), // Length
		epcd,
		//chIndex,
		//timestamp,
		//tagSeenCount,
		aptd,
	}), nil
}
