package gosstrak

import (
	"net"
	"sync"
	"sync/atomic"
)

// limitedListener bounds net/http connection goroutines without an accept
// worker or an unbounded waiting queue. Excess connections are closed at accept.
type limitedListener struct {
	net.Listener
	active atomic.Int32
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.active.Add(1) > 32 {
			l.active.Add(-1)
			_ = conn.Close()
			continue
		}
		return &limitedConn{Conn: conn, release: func() { l.active.Add(-1) }}, nil
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
