package emulator

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

func session(t *testing.T) (*Server, *fakeClock, net.Conn, context.CancelFunc, <-chan error) {
	t.Helper()
	clock := newClock()
	config := DefaultConfig()
	config.ReportInterval = 100 * time.Millisecond
	config.FrameBytes = 128
	s, err := New(config, scenario(t), clock, SeededRandom, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, peer := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	t.Cleanup(func() { cancel(); _ = client.Close(); _ = peer.Close() })
	go func() { done <- s.serve(ctx, client) }()
	return s, clock, peer, cancel, done
}
func receive(t *testing.T, c net.Conn) llrp.Message {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	m, err := llrp.ReadMessage(c, llrp.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func send(t *testing.T, c net.Conn, m llrp.Message) {
	t.Helper()
	_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := llrp.WriteMessage(c, m, llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
}
func start(t *testing.T, c net.Conn) {
	t.Helper()
	if m := receive(t, c); m.Header.Type != llrp.ReaderEventNotificationHeader {
		t.Fatal(m.Header)
	}
	send(t, c, llrp.SetReaderConfigMessage(3456))
	m := receive(t, c)
	if m.Header.Type != llrp.SetReaderConfigResponseHeader || m.Header.ID != 3456 {
		t.Fatal(m.Header)
	}
}
func TestDeterministicWireReports(t *testing.T) {
	collect := func() [][]byte {
		_, clock, peer, cancel, done := session(t)
		start(t, peer)
		var frames [][]byte
		for i := 0; i < 6; i++ {
			m := receive(t, peer)
			if m.Header.Type != llrp.ROAccessReportHeader {
				t.Fatal(m.Header)
			}
			raw, err := llrp.EncodeMessage(m, llrp.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, raw)
			eventually(t, func() bool { return clock.has(100 * time.Millisecond) })
			if i < 5 {
				clock.advance(100 * time.Millisecond)
			}
		}
		cancel()
		_ = result(t, done)
		if clock.count() != 0 {
			t.Fatal("timers leaked")
		}
		return frames
	}
	a, b := collect(), collect()
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("report %d differs", i)
		}
	}
}
func TestKeepaliveAndUpdate(t *testing.T) {
	s, clock, peer, cancel, done := session(t)
	start(t, peer)
	_ = receive(t, peer)
	eventually(t, func() bool { return clock.has(10*time.Second) && clock.has(100*time.Millisecond) })
	clock.advance(10 * time.Second)
	// Both timers are due. Reports and keepalives share one writer; report IDs
	// remain independent of this wire interleaving.
	var keepalive llrp.Message
	for i := 0; i < 2; i++ {
		m := receive(t, peer)
		if m.Header.Type == llrp.KeepaliveHeader {
			keepalive = m
		}
	}
	if keepalive.Header.Type != llrp.KeepaliveHeader {
		t.Fatal("no keepalive")
	}
	send(t, peer, llrp.KeepaliveAckMessage(keepalive.Header.ID))
	eventually(t, func() bool { return clock.has(10*time.Second) && clock.has(100*time.Millisecond) })
	replacement, err := NewScenario([][]inventory.Tag{tags(t, "0009")}, 7, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Replace(replacement); err != nil {
		t.Fatal(err)
	}
	clock.advance(100 * time.Millisecond)
	m := receive(t, peer)
	events, err := llrp.DecodeReadEvents(m.Payload, llrp.DefaultLimits())
	if err != nil || len(events) != 1 || !bytes.Equal(events[0].ID, []byte{0, 9}) {
		t.Fatal(events, err)
	}
	cancel()
	_ = result(t, done)
}
func TestMissingAndWrongKeepaliveACK(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "wrong"}[wrong], func(t *testing.T) {
			_, clock, peer, _, done := session(t)
			start(t, peer)
			_ = receive(t, peer)
			eventually(t, func() bool { return clock.count() == 2 })
			clock.advance(10 * time.Second)
			var id uint32
			for i := 0; i < 2; i++ {
				m := receive(t, peer)
				if m.Header.Type == llrp.KeepaliveHeader {
					id = m.Header.ID
				}
			}
			eventually(t, func() bool { return clock.has(5*time.Second) && clock.has(100*time.Millisecond) })
			if wrong {
				send(t, peer, llrp.KeepaliveAckMessage(id+1))
			} else {
				// Keep draining reports while the missing ACK timer expires.
				// Timeout must work even when the report timer is also due.
				drained := make(chan struct{})
				go func() {
					defer close(drained)
					for {
						if _, err := llrp.ReadMessage(peer, llrp.DefaultLimits()); err != nil {
							return
						}
					}
				}()
				defer func() { _ = peer.Close(); <-drained }()
				clock.advance(5 * time.Second)
			}
			err := result(t, done)
			if wrong && !errors.Is(err, ErrProtocol) {
				t.Fatal(err)
			}
			if !wrong && !errors.Is(err, ErrKeepaliveTimeout) {
				t.Fatal(err)
			}
		})
	}
}
func TestMalformedHandshakeAndDisconnect(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		_, _, peer, _, done := session(t)
		_ = receive(t, peer)
		if malformed {
			frame := make([]byte, 10)
			binary.BigEndian.PutUint32(frame[2:6], 1<<21)
			if _, err := peer.Write(frame); err != nil {
				t.Fatal(err)
			}
		} else {
			_ = peer.Close()
		}
		if err := result(t, done); err == nil {
			t.Fatal("accepted invalid peer")
		}
	}
}
func TestCancelBlockedWrite(t *testing.T) {
	_, _, peer, cancel, done := session(t)
	_ = peer // Do not read the notification.
	cancel()
	_ = result(t, done)
}

func TestWallClockDeadlines(t *testing.T) {
	for _, phase := range []string{"notification write", "handshake read", "report write"} {
		t.Run(phase, func(t *testing.T) {
			c := DefaultConfig()
			c.HandshakeTimeout = 30 * time.Millisecond
			c.WriteTimeout = 30 * time.Millisecond
			s, err := New(c, scenario(t), newClock(), SeededRandom, nil)
			if err != nil {
				t.Fatal(err)
			}
			client, peer := net.Pipe()
			defer peer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.serve(ctx, client) }()
			switch phase {
			case "handshake read":
				_ = receive(t, peer)
			case "report write":
				start(t, peer)
			}
			err = result(t, done)
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("expected socket timeout, got %v", err)
			}
		})
	}
}

func TestUpdateDoesNotMixReportFragments(t *testing.T) {
	initial, err := NewScenario([][]inventory.Tag{tags(t, "303400000000000000000001", "303400000000000000000002", "303400000000000000000003", "303400000000000000000004", "303400000000000000000005", "303400000000000000000006")}, 1, true, false)
	if err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.FrameBytes = 128
	c.ReportInterval = 100 * time.Millisecond
	clock := newClock()
	s, err := New(c, initial, clock, SeededRandom, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, client) }()
	start(t, peer)
	first := receive(t, peer)
	replacement, _ := NewScenario([][]inventory.Tag{tags(t, "0009")}, 1, true, false)
	if err = s.Replace(replacement); err != nil {
		t.Fatal(err)
	}
	second := receive(t, peer)
	var total int
	for _, m := range []llrp.Message{first, second} {
		events, err := llrp.DecodeReadEvents(m.Payload, llrp.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		total += len(events)
		for _, e := range events {
			if len(e.ID) != 12 {
				t.Fatal("update mixed into current cycle")
			}
		}
	}
	if total != 6 {
		t.Fatal(total)
	}
	eventually(t, func() bool { return clock.has(c.ReportInterval) })
	clock.advance(c.ReportInterval)
	next := receive(t, peer)
	events, err := llrp.DecodeReadEvents(next.Payload, llrp.DefaultLimits())
	if err != nil || len(events) != 1 || !bytes.Equal(events[0].ID, []byte{0, 9}) {
		t.Fatal(events, err)
	}
	cancel()
	_ = result(t, done)
}

func TestLateACKWhileReportWriteIsBlocked(t *testing.T) {
	_, clock, peer, _, done := session(t)
	start(t, peer)
	_ = receive(t, peer)
	eventually(t, func() bool { return clock.has(10*time.Second) && clock.has(100*time.Millisecond) })
	clock.advance(10 * time.Second)
	var id uint32
	for i := 0; i < 2; i++ {
		m := receive(t, peer)
		if m.Header.Type == llrp.KeepaliveHeader {
			id = m.Header.ID
		}
	}
	eventually(t, func() bool { return clock.has(5*time.Second) && clock.has(100*time.Millisecond) })
	clock.advance(100 * time.Millisecond)
	// Read one byte to prove the report write has started, leaving it blocked.
	prefix := make([]byte, 1)
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(peer, prefix); err != nil {
		t.Fatal(err)
	}
	clock.advance(5 * time.Second)
	send(t, peer, llrp.KeepaliveAckMessage(id))
	if _, err := llrp.ReadMessage(io.MultiReader(bytes.NewReader(prefix), peer), llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if err := result(t, done); !errors.Is(err, ErrKeepaliveTimeout) {
		t.Fatal(err)
	}
}
