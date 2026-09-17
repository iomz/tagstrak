package emulator

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/reader"
)

func launch(t *testing.T, c Config, clock Clock) (*Server, context.CancelFunc, <-chan error) {
	t.Helper()
	c.Address = "127.0.0.1:0"
	s, err := New(c, scenario(t), clock, SeededRandom, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	eventually(t, func() bool { return s.Status().Running })
	return s, cancel, done
}
func TestServerReaderContract(t *testing.T) {
	clock := newClock()
	config := DefaultConfig()
	config.ReportInterval = time.Hour
	s, stopServer, serverDone := launch(t, config, clock)
	rc := reader.DefaultConfig()
	rc.Address = s.Status().Address
	rc.MaxRetries = 0
	client, err := reader.New(rc, &net.Dialer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientDone := make(chan error, 1)
	out := make(chan inventory.Observation, 32)
	go func() { clientDone <- client.Run(ctx, out) }()
	for i := 0; i < 3; i++ {
		select {
		case observation := <-out:
			if observation.Source() != rc.Source {
				t.Fatal(observation)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("reader did not ingest scenario")
		}
	}
	eventually(t, func() bool { return clock.has(10*time.Second) && clock.has(time.Hour) })
	clock.advance(10 * time.Second)
	eventually(t, func() bool { return client.Snapshot().Keepalives == 1 && clock.has(10*time.Second) })
	replacement, err := NewScenario([][]inventory.Tag{tags(t, "0009")}, 9, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Replace(replacement); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Hour)
	select {
	case observation := <-out:
		if observation.Tag().Hex() != "0009" {
			t.Fatal(observation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not ingest update")
	}
	cancel()
	if err = result(t, clientDone); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	eventually(t, func() bool { return s.Status().Clients == 0 })
	stopServer()
	if err = result(t, serverDone); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.Status().Running || clock.count() != 0 {
		t.Fatal(s.Status(), clock.count())
	}
}
func TestClientLimitAndShutdown(t *testing.T) {
	c := DefaultConfig()
	c.MaxClients = 1
	s, cancel, done := launch(t, c, SystemClock{})
	first, err := net.Dial("tcp", s.Status().Address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	eventually(t, func() bool { return s.Status().Clients == 1 })
	second, err := net.Dial("tcp", s.Status().Address)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = second.Read(make([]byte, 1)); err == nil {
		t.Fatal("excess client received data")
	}
	eventually(t, func() bool { return s.Status().Rejected == 1 })
	cancel()
	if err = result(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.Status().Clients != 0 {
		t.Fatal(s.Status())
	}
}
func TestInvalidConfig(t *testing.T) {
	mutations := []func(*Config){func(c *Config) { c.MaxClients = 0 }, func(c *Config) { c.ReportInterval = 0 }, func(c *Config) { c.FrameBytes = 127 }, func(c *Config) { c.ReadTimeout = c.KeepaliveInterval }, func(c *Config) { c.Address = "localhost:99999" }}
	for _, mutate := range mutations {
		c := DefaultConfig()
		mutate(&c)
		if _, err := New(c, scenario(t), SystemClock{}, SeededRandom, nil); !errors.Is(err, ErrConfig) {
			t.Fatal(err)
		}
	}
}
