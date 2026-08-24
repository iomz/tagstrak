package llrp

import (
	"bytes"
	"testing"
)

func TestTypedMessagesEncodeLikeLegacyBuilders(t *testing.T) {
	trd, err := NewTagReportDataParam(make([]byte, 12), 0x3000)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		new  Message
		old  []byte
	}{
		{"keepalive", KeepaliveMessage(7), Keepalive(7)},
		{"keepalive ack", KeepaliveAckMessage(7), KeepaliveAck(7)},
		{"reader event", ReaderEventNotificationMessage(7, 42), ReaderEventNotification(7, 42)},
		{"set config", SetReaderConfigMessage(7), SetReaderConfig(7)},
		{"set config response", SetReaderConfigResponseMessage(7), SetReaderConfigResponse(7)},
		{"ro access report", ROAccessReportMessage(trd, 7), NewROAccessReport(trd, 7).data},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := EncodeMessage(tt.new, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, tt.old) {
				t.Fatalf("encoded = %v, legacy = %v", encoded, tt.old)
			}
		})
	}
}
