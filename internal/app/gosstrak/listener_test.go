package gosstrak

import (
	"errors"
	"net"
	"testing"
)

type scriptedListener struct {
	net.Listener
	connections []net.Conn
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	if len(l.connections) == 0 {
		return nil, net.ErrClosed
	}
	c := l.connections[0]
	l.connections = l.connections[1:]
	return c, nil
}
func TestTelemetryConnectionLimit(t *testing.T) {
	source := &scriptedListener{}
	for i := 0; i < 33; i++ {
		client, peer := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
		source.connections = append(source.connections, client)
	}
	listener := &limitedListener{Listener: source}
	var accepted []net.Conn
	for i := 0; i < 32; i++ {
		c, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, c)
	}
	if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if listener.active.Load() != 32 {
		t.Fatal(listener.active.Load())
	}
	for _, c := range accepted {
		_ = c.Close()
		_ = c.Close()
	}
	if listener.active.Load() != 0 {
		t.Fatal("connections not released exactly once")
	}
}
