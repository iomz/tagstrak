package llrp

import (
	"encoding/hex"
	"testing"
)

// TestTypedMessagesWireBytes pins each typed message's encoding to the bytes
// the v1 builders produced, captured before those builders were removed.
func TestTypedMessagesWireBytes(t *testing.T) {
	trd, err := NewTagReportDataParam(make([]byte, 12), 0x3000)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		message Message
		want    string
	}{
		{"keepalive", KeepaliveMessage(7), "043e0000000a00000007"},
		{"keepalive ack", KeepaliveAckMessage(7), "04480000000a00000007"},
		{"reader event", ReaderEventNotificationMessage(7, 42), "043f000000200000000700f600160080000c000000000000002a010000060000"},
		{"set config", SetReaderConfigMessage(7), "040300000014000000070000dc00090100002710"},
		{"set config response", SetReaderConfigResponseMessage(7), "040d0000001200000007011f000800000000"},
		{"ro access report", ROAccessReportMessage(trd, 7), "043d0000001e0000000700f000148d0000000000000000000000008c3000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := EncodeMessage(tt.message, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			if got := hex.EncodeToString(encoded); got != tt.want {
				t.Fatalf("encoded %s, want %s", got, tt.want)
			}
		})
	}
}
