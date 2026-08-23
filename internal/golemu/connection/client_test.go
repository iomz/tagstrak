//
// Use of this source code is governed by The MIT License
// that can be found in the LICENSE file.

package connection

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/llrp"
)

func TestNewClient(t *testing.T) {
	client := NewClient("127.0.0.1", 5084)

	if client == nil {
		t.Fatal("NewClient returned nil")
	}
	if client.ip != "127.0.0.1" {
		t.Errorf("expected ip 127.0.0.1, got %s", client.ip)
	}
	if client.port != 5084 {
		t.Errorf("expected port 5084, got %d", client.port)
	}
}

func TestClient_handleMessage_ReaderEventNotification(t *testing.T) {
	client := NewClient("127.0.0.1", 5084)

	var writeBuf bytes.Buffer
	conn := &mockConn{writer: &writeBuf}

	messageID := uint32(1001)
	client.handleMessage(conn, llrp.Message{Header: llrp.Header{Type: llrp.ReaderEventNotificationHeader, ID: messageID}})

	// Verify response was written
	if writeBuf.Len() == 0 {
		t.Error("expected SET_READER_CONFIG to be written")
	}
}

func TestClient_handleMessage_Keepalive(t *testing.T) {
	client := NewClient("127.0.0.1", 5084)

	var writeBuf bytes.Buffer
	conn := &mockConn{writer: &writeBuf}

	messageID := uint32(1001)
	client.handleMessage(conn, llrp.Message{Header: llrp.Header{Type: llrp.KeepaliveHeader, ID: messageID}})

	// Verify response was written
	if writeBuf.Len() == 0 {
		t.Error("expected KEEP_ALIVE_ACK to be written")
	}
}

func TestClient_handleMessage_SetReaderConfigResponse(t *testing.T) {
	client := NewClient("127.0.0.1", 5084)

	var writeBuf bytes.Buffer
	conn := &mockConn{writer: &writeBuf}

	messageID := uint32(1001)
	client.handleMessage(conn, llrp.Message{Header: llrp.Header{Type: llrp.SetReaderConfigResponseHeader, ID: messageID}})

	// Should not write anything for response messages
	if writeBuf.Len() != 0 {
		t.Error("expected no data to be written for SET_READER_CONFIG_RESPONSE")
	}
}

func TestClient_handleMessage_ROAccessReport(t *testing.T) {
	client := NewClient("127.0.0.1", 5084)

	var writeBuf bytes.Buffer
	conn := &mockConn{writer: &writeBuf}

	messageID := uint32(1001)
	// One decodable TagReportData parameter with a 96-bit EPC.
	messageValue := llrp.NewTagReportDataParam(make([]byte, 12), 0x3000)

	client.handleMessage(conn, llrp.Message{Header: llrp.Header{Type: llrp.ROAccessReportHeader, ID: messageID}, Payload: messageValue})

	// Should not write anything for RO_ACCESS_REPORT
	if writeBuf.Len() != 0 {
		t.Error("expected no data to be written for RO_ACCESS_REPORT")
	}
}

func TestClient_handleMessage_UnknownHeader(t *testing.T) {
	client := NewClient("127.0.0.1", 5084)

	var writeBuf bytes.Buffer
	conn := &mockConn{writer: &writeBuf}

	messageID := uint32(1001)
	unknownHeader := uint16(0xFFFF)

	client.handleMessage(conn, llrp.Message{Header: llrp.Header{Type: unknownHeader, ID: messageID}})

	// Should not write anything for unknown headers
	if writeBuf.Len() != 0 {
		t.Error("expected no data to be written for unknown header")
	}
}

func TestClient_Run_EOF(t *testing.T) {
	client := NewClient("127.0.0.1", 5084)

	// Test that handleMessage works with various message types
	var writeBuf bytes.Buffer
	conn := &mockConn{writer: &writeBuf}

	// Test READER_EVENT_NOTIFICATION
	currentTime := uint64(time.Now().UTC().Nanosecond() / 1000)
	msg := llrp.ReaderEventNotification(1001, currentTime)
	var msgBuf bytes.Buffer
	msgBuf.Write(msg)

	message, err := llrp.ReadMessage(&mockConn{reader: &msgBuf}, llrp.DefaultLimits())
	if err != nil {
		t.Fatalf("ReadMessage failed: %v", err)
	}

	client.handleMessage(conn, message)
	if writeBuf.Len() == 0 {
		t.Error("expected response to be written")
	}
}

func TestClient_Run_ReadError(t *testing.T) {
	// Create a message buffer that will cause a read error
	var buf bytes.Buffer
	// Write incomplete header (only 5 bytes)
	buf.Write([]byte{0x00, 0x01, 0x00, 0x00, 0x00})

	conn := &mockConn{reader: &buf}

	// This would be called in Run() loop
	message, err := llrp.ReadMessage(conn, llrp.DefaultLimits())
	if err == nil {
		t.Error("expected error for incomplete message")
	}
	if message.Header != (llrp.Header{}) {
		t.Error("expected zero message on error")
	}
}

// errorReader is a reader that always returns an error
type errorReader struct{}

func (e *errorReader) Read(p []byte) (n int, err error) {
	return 0, io.ErrUnexpectedEOF
}
