package gosstrak

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/reader"
	"github.com/iomz/tagstrak/v2/llrp"
)

type consumerFunc func(context.Context, inventory.Observation) error

func (f consumerFunc) Consume(ctx context.Context, o inventory.Observation) error { return f(ctx, o) }

type dialFunc func(context.Context, string, string) (net.Conn, error)

func (f dialFunc) DialContext(ctx context.Context, n, a string) (net.Conn, error) {
	return f(ctx, n, a)
}

func config() Config {
	c := DefaultConfig()
	c.TelemetryAddress = "127.0.0.1:0"
	c.Reader.MaxRetries = 0
	c.Reader.ReadTimeout = time.Second
	c.ShutdownTimeout = 200 * time.Millisecond
	return c
}
func await(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
		return nil
	}
}
func eventually(t *testing.T, f func() bool) {
	t.Helper()
	end := time.Now().Add(time.Second)
	for !f() {
		if time.Now().After(end) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
func connect(t *testing.T, c net.Conn) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if e := llrp.WriteMessage(c, llrp.ReaderEventNotificationMessage(1, 1), llrp.DefaultLimits()); e != nil {
		t.Fatal(e)
	}
	req, e := llrp.ReadMessage(c, llrp.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	if e = llrp.WriteMessage(c, llrp.SetReaderConfigResponseMessage(req.Header.ID), llrp.DefaultLimits()); e != nil {
		t.Fatal(e)
	}
}
func sendReport(t *testing.T, c net.Conn) {
	t.Helper()
	epc := llrp.EPCData(0, 96, []byte{0x30, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1})
	p, e := llrp.EncodeParameter(llrp.Parameter{Type: 240, Data: epc})
	if e != nil {
		t.Fatal(e)
	}
	if e = llrp.WriteMessage(c, llrp.ROAccessReportMessage(p, 7), llrp.DefaultLimits()); e != nil {
		t.Fatal(e)
	}
}
func pipeApp(t *testing.T, c Config, consumer Consumer) (*App, net.Conn) {
	t.Helper()
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	a, e := New(c, dialFunc(func(context.Context, string, string) (net.Conn, error) { return client, nil }), consumer, nil)
	if e != nil {
		t.Fatal(e)
	}
	return a, peer
}
func statusCode(a *App, path string) int {
	r := httptest.NewRecorder()
	a.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
	return r.Code
}

func TestReadinessAndGracefulDrain(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var count atomic.Int32
	a, peer := pipeApp(t, config(), consumerFunc(func(ctx context.Context, o inventory.Observation) error {
		if count.Add(1) == 1 {
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}))
	if statusCode(a, "/healthz") != 503 || statusCode(a, "/readyz") != 503 {
		t.Fatal("ready before run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	connect(t, peer)
	eventually(t, func() bool { return a.Status().Ready })
	if statusCode(a, "/healthz") != 200 || statusCode(a, "/readyz") != 200 {
		t.Fatal(a.Status())
	}
	sendReport(t, peer)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("consumer not called")
	}
	sendReport(t, peer)
	eventually(t, func() bool { return a.Status().QueueDepth == 1 })
	cancel()
	eventually(t, func() bool { return a.Status().Draining })
	if statusCode(a, "/readyz") != 503 {
		t.Fatal("ready during drain")
	}
	close(release)
	if err := await(t, done); !errors.Is(err, context.Canceled) || errors.Is(err, ErrDrainDeadline) {
		t.Fatal(err)
	}
	if a.Status().Processed != 2 || a.Status().Abandoned != 0 || a.Status().Healthy {
		t.Fatal(a.Status())
	}
	r := httptest.NewRecorder()
	a.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(r.Body.String(), "gosstrak_observations_processed_total 2\n") {
		t.Fatal(r.Body.String())
	}
}

func TestDrainDeadlineCancelsConsumer(t *testing.T) {
	entered := make(chan struct{})
	c := config()
	c.ShutdownTimeout = 30 * time.Millisecond
	a, peer := pipeApp(t, c, consumerFunc(func(ctx context.Context, _ inventory.Observation) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	connect(t, peer)
	sendReport(t, peer)
	<-entered
	sendReport(t, peer)
	eventually(t, func() bool { return a.Status().QueueDepth == 1 })
	cancel()
	if err := await(t, done); !errors.Is(err, ErrDrainDeadline) {
		t.Fatal(err)
	}
	if a.Status().Abandoned != 2 || a.Status().QueueDepth != 0 || a.Status().Ready {
		t.Fatal(a.Status())
	}
}

func TestConsumerFailureStopsReader(t *testing.T) {
	failure := errors.New("test consumer failure")
	a, peer := pipeApp(t, config(), consumerFunc(func(context.Context, inventory.Observation) error { return failure }))
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	connect(t, peer)
	sendReport(t, peer)
	if err := await(t, done); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if a.Status().Ready || a.Status().Abandoned != 1 {
		t.Fatal(a.Status())
	}
}

func TestConsumeTimeout(t *testing.T) {
	c := config()
	c.ConsumeTimeout = 20 * time.Millisecond
	a, peer := pipeApp(t, c, consumerFunc(func(ctx context.Context, _ inventory.Observation) error { <-ctx.Done(); return ctx.Err() }))
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	connect(t, peer)
	sendReport(t, peer)
	if err := await(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestListenerFailurePreventsDial(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := config()
	c.TelemetryAddress = listener.Addr().String()
	var dialed atomic.Bool
	a, err := New(c, dialFunc(func(context.Context, string, string) (net.Conn, error) { dialed.Store(true); return nil, io.EOF }), consumerFunc(func(context.Context, inventory.Observation) error { return nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Run(context.Background()); err == nil {
		t.Fatal("listener unexpectedly opened")
	}
	if dialed.Load() {
		t.Fatal("dialed before successful startup")
	}
}

func TestTCPDisconnectReconnectAndTelemetry(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := config()
	c.Reader.Address = listener.Addr().String()
	c.Reader.MaxRetries = 1
	c.Reader.RetryMin = time.Millisecond
	c.Reader.RetryMax = time.Millisecond
	a, err := New(c, &net.Dialer{}, consumerFunc(func(context.Context, inventory.Observation) error { return nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background()) }()
	// Real TCP exercises EOF-driven reconnect and resource cleanup beyond net.Pipe.
	for i := 0; i < 2; i++ {
		_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(2 * time.Second))
		peer, e := listener.Accept()
		if e != nil {
			t.Fatal(e)
		}
		connect(t, peer)
		sendReport(t, peer)
		_ = peer.Close()
	}
	if err = await(t, done); !errors.Is(err, reader.ErrRetryExhausted) {
		t.Fatal(err)
	}
	if a.Status().Reader.Attempts != 2 || a.Status().Processed != 2 || a.Status().Ready {
		t.Fatal(a.Status())
	}
}

func TestHTTPLifecycle(t *testing.T) {
	a, peer := pipeApp(t, config(), consumerFunc(func(context.Context, inventory.Observation) error { return nil }))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	connect(t, peer)
	eventually(t, func() bool { return a.Status().Ready })
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: time.Second}
	url := "http://" + a.Status().TelemetryAddress + "/readyz"
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	cancel()
	if err = await(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if response, err = client.Get(url); err == nil {
		_ = response.Body.Close()
		t.Fatal("HTTP listener survived shutdown")
	}
}
