package reader

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

type dialFunc func(context.Context, string, string) (net.Conn, error)

func (f dialFunc) DialContext(c context.Context, n, a string) (net.Conn, error) { return f(c, n, a) }
func testConfig() Config {
	c := DefaultConfig()
	c.ConnectTimeout = time.Second
	c.HandshakeTimeout = 100 * time.Millisecond
	c.ReadTimeout = 100 * time.Millisecond
	c.WriteTimeout = 100 * time.Millisecond
	c.RetryMin = time.Millisecond
	c.RetryMax = 4 * time.Millisecond
	c.MaxRetries = 0
	return c
}
func readPeer(conn net.Conn) (llrp.Message, error) {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	return llrp.ReadMessage(conn, llrp.DefaultLimits())
}
func handshake(conn net.Conn) error {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := llrp.WriteMessage(conn, llrp.ReaderEventNotificationMessage(99, 1), llrp.DefaultLimits()); err != nil {
		return err
	}
	request, err := readPeer(conn)
	if err != nil {
		return err
	}
	if request.Header.Type != llrp.SetReaderConfigHeader {
		return ErrProtocol
	}
	return llrp.WriteMessage(conn, llrp.SetReaderConfigResponseMessage(request.Header.ID), llrp.DefaultLimits())
}
func report(t *testing.T) llrp.Message {
	t.Helper()
	epc := llrp.EPCData(0, 96, []byte{0x30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	param, err := llrp.EncodeParameter(llrp.Parameter{Type: 240, Data: epc})
	if err != nil {
		t.Fatal(err)
	}
	return llrp.ROAccessReportMessage(param, 100)
}
func pipeSession(t *testing.T, c Config) (*Session, net.Conn) {
	t.Helper()
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	s, err := New(c, dialFunc(func(context.Context, string, string) (net.Conn, error) { return client, nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, peer
}
func waitResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
		return nil
	}
}

func TestSessionReportsKeepaliveAndShutdown(t *testing.T) {
	s, peer := pipeSession(t, testConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan inventory.Observation, 2)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, out) }()
	if err := handshake(peer); err != nil {
		t.Fatal(err)
	}
	if err := llrp.WriteMessage(peer, llrp.KeepaliveMessage(12345), llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	ack, err := readPeer(peer)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Header.Type != llrp.KeepaliveAckHeader || ack.Header.ID != 12345 {
		t.Fatalf("ack: %+v", ack)
	}
	if err := llrp.WriteMessage(peer, report(t), llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-out:
		if o.Source() != s.config.Source || o.Tag().Hex() != "300000000000000000000001" || o.SeenAt().IsZero() {
			t.Fatalf("observation: %+v", o)
		}
	case <-time.After(time.Second):
		t.Fatal("no observation")
	}
	cancel()
	if err := waitResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	snapshot := s.Snapshot()
	if snapshot.State != Stopped || snapshot.Ready || snapshot.Keepalives != 1 || snapshot.Observations != 1 {
		t.Fatalf("status: %+v", snapshot)
	}
	if err := s.Run(context.Background(), out); !errors.Is(err, ErrAlreadyRun) {
		t.Fatal(err)
	}
}

func TestSessionFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(net.Conn) error
		want error
	}{
		{"disconnect", func(c net.Conn) error { return c.Close() }, ErrRetryExhausted},
		{"handshake timeout", func(c net.Conn) error { _, err := readPeer(c); return err }, ErrRetryExhausted},
		{"idle timeout", func(c net.Conn) error {
			if err := handshake(c); err != nil {
				return err
			}
			_, err := readPeer(c)
			return err
		}, ErrRetryExhausted},
		{"oversized frame", func(c net.Conn) error {
			frame := make([]byte, 10)
			binary.BigEndian.PutUint32(frame[2:6], 1<<25)
			_, err := c.Write(frame)
			return err
		}, llrp.ErrFrameTooLarge},
		{"short frame length", func(c net.Conn) error { _, err := c.Write(make([]byte, 10)); return err }, llrp.ErrMalformedHeader},
		{"wrong response id", func(c net.Conn) error {
			if err := llrp.WriteMessage(c, llrp.ReaderEventNotificationMessage(1, 1), llrp.DefaultLimits()); err != nil {
				return err
			}
			if _, err := readPeer(c); err != nil {
				return err
			}
			return llrp.WriteMessage(c, llrp.SetReaderConfigResponseMessage(7), llrp.DefaultLimits())
		}, ErrProtocol},
		{"negative status", func(c net.Conn) error {
			if err := llrp.WriteMessage(c, llrp.ReaderEventNotificationMessage(1, 1), llrp.DefaultLimits()); err != nil {
				return err
			}
			req, err := readPeer(c)
			if err != nil {
				return err
			}
			m := llrp.SetReaderConfigResponseMessage(req.Header.ID)
			m.Payload[5] = 1
			return llrp.WriteMessage(c, m, llrp.DefaultLimits())
		}, ErrProtocol},
		{"malformed report", func(c net.Conn) error {
			if err := handshake(c); err != nil {
				return err
			}
			return llrp.WriteMessage(c, llrp.ROAccessReportMessage([]byte{0, 240, 0, 3}, 3), llrp.DefaultLimits())
		}, ErrProtocol},
		{"unknown message", func(c net.Conn) error {
			if err := handshake(c); err != nil {
				return err
			}
			return llrp.WriteMessage(c, llrp.Message{Header: llrp.Header{Type: 0xffff}}, llrp.DefaultLimits())
		}, ErrProtocol},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, peer := pipeSession(t, testConfig())
			done := make(chan error, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { done <- s.Run(ctx, make(chan inventory.Observation, 1)) }()
			_ = tc.run(peer)
			err := waitResult(t, done)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if s.Snapshot().State != Failed || s.Snapshot().Ready {
				t.Fatal(s.Snapshot())
			}
		})
	}
}

func TestSlowWriterDeadlineAndCancellation(t *testing.T) {
	for _, cancelWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[cancelWrite], func(t *testing.T) {
			s, peer := pipeSession(t, testConfig())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.Run(ctx, make(chan inventory.Observation, 1)) }()
			if err := llrp.WriteMessage(peer, llrp.ReaderEventNotificationMessage(1, 1), llrp.DefaultLimits()); err != nil {
				t.Fatal(err)
			}
			// Do not read the config request: net.Pipe forces its write to block.
			if cancelWrite {
				cancel()
			}
			err := waitResult(t, done)
			if cancelWrite && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if !cancelWrite && !errors.Is(err, ErrRetryExhausted) {
				t.Fatal(err)
			}
		})
	}
}

func TestQueueOverflowIsTerminal(t *testing.T) {
	c := testConfig()
	c.MaxRetries = 10
	s, peer := pipeSession(t, c)
	out := make(chan inventory.Observation, 1)
	tag, _ := inventory.ParseTag("0001")
	observation, _ := inventory.NewObservation(tag, time.Now(), "test")
	out <- observation
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background(), out) }()
	if err := handshake(peer); err != nil {
		t.Fatal(err)
	}
	if err := llrp.WriteMessage(peer, report(t), llrp.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, done); !errors.Is(err, ErrBackpressure) {
		t.Fatal(err)
	}
	if s.Snapshot().Attempts != 1 || s.Snapshot().QueueOverflows != 1 || len(out) != 1 {
		t.Fatal(s.Snapshot())
	}
}

func TestReconnectBudgetSurvivesSuccessfulHandshakes(t *testing.T) {
	c := testConfig()
	c.MaxRetries = 2
	var peers sync.WaitGroup
	s, err := New(c, dialFunc(func(context.Context, string, string) (net.Conn, error) {
		client, peer := net.Pipe()
		peers.Add(1)
		go func() { defer peers.Done(); defer peer.Close(); _ = handshake(peer) }()
		return client, nil
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Run(context.Background(), make(chan inventory.Observation, 1)); !errors.Is(err, ErrRetryExhausted) {
		t.Fatal(err)
	}
	peers.Wait()
	if s.Snapshot().Attempts != 3 || s.Snapshot().Reconnects != 2 {
		t.Fatal(s.Snapshot())
	}
}

func TestCancelDialAndBackoff(t *testing.T) {
	for _, inDial := range []bool{true, false} {
		c := testConfig()
		c.RetryMin = time.Hour
		c.RetryMax = time.Hour
		c.MaxRetries = 2
		entered := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		s, _ := New(c, dialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
			close(entered)
			if inDial {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, io.EOF
		}), nil)
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx, make(chan inventory.Observation, 1)) }()
		<-entered
		if !inDial {
			deadline := time.After(time.Second)
			for s.Snapshot().State != Reconnecting {
				select {
				case <-deadline:
					t.Fatal("no reconnect state")
				default:
					time.Sleep(time.Millisecond)
				}
			}
		}
		cancel()
		if err := waitResult(t, done); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}
func TestBackoff(t *testing.T) {
	c := testConfig()
	got := []time.Duration{}
	for i := 1; i <= 5; i++ {
		got = append(got, backoff(c, i))
	}
	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
