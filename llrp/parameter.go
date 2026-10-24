package llrp

// c1G2PC returns a C1G2_PC parameter constructed from the provided PC bits.
func c1G2PC(pc uint16) []byte {
	var data = []interface{}{
		uint8(140), // 1+uint7(Type=12)
		pc,         // PC bits
	}
	return pack(data)
}

// connectionAttemptEvent returns a CONNECTION_ATTEMPT_EVENT parameter indicating success.
func connectionAttemptEvent() []byte {
	var data = []interface{}{
		uint16(256), // Rsvd+Type=256
		uint16(6),   // Length
		uint16(0),   // status(Success=0)
	}
	return pack(data)
}

// EPCData returns an EPC_DATA parameter for the supplied EPC value and length.
func EPCData(length uint16, epcLengthBits uint16, epc []byte) []byte {
	var data []interface{}
	if epcLengthBits == 96 {
		data = []interface{}{
			uint8(141), // 1+uint7(Type=13)
			epc,        // 96-bit EPCData string
		}
	} else {
		data = []interface{}{
			uint16(241),           // uint8(0)+uint8(Type=241)
			uint16(length),        // Length
			uint16(epcLengthBits), // EPCLengthBits
			epc,                   // EPCData string
		}
	}
	return pack(data)
}

// keepaliveSpec returns a KEEPALIVE_SPEC parameter for periodic keepalives.
func keepaliveSpec() []byte {
	var data = []interface{}{
		uint16(220),   // Rsvd+Type=220
		uint16(9),     // Length
		uint8(1),      // KeepaliveTriggerType=Periodic(1)
		uint32(10000), // TimeInterval=10000
	}
	return pack(data)
}

// status returns an LLRP_STATUS parameter indicating success.
func status() []byte {
	var data = []interface{}{
		uint16(287), // Rsvd+Type=287
		uint16(8),   // Length
		uint16(0),   // StatusCode=M_Success(0)
		uint16(0),   // ErrorDescriptionByteCount=0
	}
	return pack(data)
}

// readerEventNotificationData returns a READER_EVENT_NOTIFICATION_DATA parameter for the provided timestamp.
func readerEventNotificationData(currentTime uint64) []byte {
	utcTimeStamp := uTCTimeStamp(currentTime)
	connectionAttemptEvent := connectionAttemptEvent()
	readerEventNotificationDataLength := len(utcTimeStamp) +
		len(connectionAttemptEvent) + 4 // Rsvd+Type+length=32bits=4bytes
	var data = []interface{}{
		uint16(246), // Rsvd+Type=246 (readerEventNotificationData parameter)
		uint16(readerEventNotificationDataLength), // Length
		utcTimeStamp,
		connectionAttemptEvent,
	}
	return pack(data)
}

// uTCTimeStamp returns a UTC_TIMESTAMP parameter for the provided time in microseconds.
func uTCTimeStamp(currentTime uint64) []byte {
	var data = []interface{}{
		uint16(128), // Rsvd+Type=128
		uint16(12),  // Length
		currentTime, // Microseconds
	}
	return pack(data)
}
