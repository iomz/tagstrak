package llrp

// KeepaliveMessage builds a typed KEEPALIVE message.
func KeepaliveMessage(messageID uint32) Message {
	return Message{Header: Header{Type: KeepaliveHeader, ID: messageID}}
}

// KeepaliveAckMessage builds a typed KEEPALIVE_ACK message.
func KeepaliveAckMessage(messageID uint32) Message {
	return Message{Header: Header{Type: KeepaliveAckHeader, ID: messageID}}
}

// ReaderEventNotificationMessage builds a typed reader event notification.
func ReaderEventNotificationMessage(messageID uint32, currentTime uint64) Message {
	return Message{
		Header:  Header{Type: ReaderEventNotificationHeader, ID: messageID},
		Payload: ReaderEventNotificationData(currentTime),
	}
}

// SetReaderConfigMessage builds a typed SET_READER_CONFIG message.
func SetReaderConfigMessage(messageID uint32) Message {
	return Message{
		Header:  Header{Type: SetReaderConfigHeader, ID: messageID},
		Payload: append([]byte{0}, KeepaliveSpec()...),
	}
}

// SetReaderConfigResponseMessage builds a typed SET_READER_CONFIG_RESPONSE.
func SetReaderConfigResponseMessage(messageID uint32) Message {
	return Message{
		Header:  Header{Type: SetReaderConfigResponseHeader, ID: messageID},
		Payload: Status(),
	}
}

// ROAccessReportMessage builds a typed RO_ACCESS_REPORT message.
func ROAccessReportMessage(tagReportData []byte, messageID uint32) Message {
	return Message{
		Header:  Header{Type: ROAccessReportHeader, ID: messageID},
		Payload: append([]byte(nil), tagReportData...),
	}
}
