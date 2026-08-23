package connection

import (
	"bytes"
	"io"
	"net"
	"sync"
	"time"
)

// mockConn is a small net.Conn test double shared by connection tests.
type mockConn struct {
	reader io.Reader
	writer io.Writer
	mu     sync.Mutex
}

func (m *mockConn) Read(b []byte) (int, error) {
	if m.reader == nil {
		return 0, io.EOF
	}
	return m.reader.Read(b)
}

func (m *mockConn) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.writer == nil {
		return len(b), nil
	}
	return m.writer.Write(b)
}

func (m *mockConn) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if buf, ok := m.writer.(*bytes.Buffer); ok {
		return buf.Len()
	}
	return 0
}

func (m *mockConn) Close() error                     { return nil }
func (m *mockConn) LocalAddr() net.Addr              { return &mockAddr{} }
func (m *mockConn) RemoteAddr() net.Addr             { return &mockAddr{} }
func (m *mockConn) SetDeadline(time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(time.Time) error { return nil }

type mockAddr struct{}

func (*mockAddr) Network() string { return "tcp" }
func (*mockAddr) String() string  { return "127.0.0.1:1234" }
