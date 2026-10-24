package httpreport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/report"
)

// batch builds a real batch by running one event through a dispatcher, so the
// body is exactly what production sends.
func batch(t *testing.T) report.Batch {
	t.Helper()
	var got report.Batch
	capture := reporterFunc(func(_ context.Context, b report.Batch) error { got = b; return nil })
	d, err := report.New(report.DefaultConfig(), []report.Destination{{Name: "capture", Reporter: capture}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Start()
	d.Enqueue("capture", report.Event{ID: "i:1", Source: "reader-1", ObservedAt: time.Unix(1, 0), Subscriptions: []string{"product"}, Scheme: "sgtin-96", EPC: []byte{0x30}})
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return got
}

type reporterFunc func(context.Context, report.Batch) error

func (f reporterFunc) Deliver(ctx context.Context, b report.Batch) error { return f(ctx, b) }

// withSecret returns the server URL with credentials in its user info, path
// and query, so tests can check none of them leaks into errors. Transport
// errors may still name host:port, which carries no credentials.
func withSecret(serverURL string) string {
	return strings.Replace(serverURL, "http://", "http://user:s3cret@", 1) + "/hooks/t0ken?key=k3y"
}

func assertNoSecret(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, secret := range []string{"s3cret", "t0ken", "k3y", "user:"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks %q from the destination URL: %v", secret, err)
		}
	}
}

func TestDeliverPostsBatchJSON(t *testing.T) {
	b := batch(t)
	var gotBody []byte
	var gotHeader http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeader = r.Header.Clone()
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	r, err := New(server.URL+"/reports", WithHeader(func(h http.Header) { h.Set("Authorization", "Bearer token") }))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != string(b.JSON()) {
		t.Fatalf("body %s", gotBody)
	}
	var body struct {
		Schema string `json:"schema"`
		Events []struct {
			ID string `json:"id"`
		} `json:"events"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil || body.Schema != report.Schema || len(body.Events) != 1 || body.Events[0].ID != "i:1" {
		t.Fatalf("body %s (%v)", gotBody, err)
	}
	for name, want := range map[string]string{"Content-Type": "application/json", "Idempotency-Key": b.ID, "Authorization": "Bearer token"} {
		if got := gotHeader.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestStatusClassification(t *testing.T) {
	b := batch(t)
	for _, tc := range []struct {
		status     int
		retryAfter string
		permanent  bool
		hint       bool
	}{
		{http.StatusServiceUnavailable, "3", false, true},
		{http.StatusTooManyRequests, "", false, false},
		{http.StatusRequestTimeout, "", false, false},
		{http.StatusInternalServerError, "", false, false},
		{http.StatusBadRequest, "", true, false},
		{http.StatusUnauthorized, "", true, false},
		{http.StatusFound, "", true, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			if tc.status == http.StatusFound {
				w.Header().Set("Location", "/elsewhere")
			}
			w.WriteHeader(tc.status)
		}))
		r, err := New(withSecret(server.URL))
		if err != nil {
			t.Fatal(err)
		}
		err = r.Deliver(context.Background(), b)
		server.Close()
		if err == nil {
			t.Fatalf("%d: delivered", tc.status)
		}
		if report.IsPermanent(err) != tc.permanent {
			t.Errorf("%d: permanent = %v, want %v", tc.status, report.IsPermanent(err), tc.permanent)
		}
		assertNoSecret(t, err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusTemporaryRedirect))
	defer server.Close()
	r, err := New(server.URL, WithHeader(func(h http.Header) { h.Set("Authorization", "Bearer token") }))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Deliver(context.Background(), batch(t)); !report.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if followed.Load() {
		t.Fatal("redirect was followed with the request's credentials")
	}
}

// TestSlowDestinationTimesOut checks that the dispatcher's per-attempt
// deadline ends a stalled request with a retryable error.
func TestSlowDestinationTimesOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	r, err := New(withSecret(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = r.Deliver(ctx, batch(t))
	if err == nil || report.IsPermanent(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	assertNoSecret(t, err)
}

func TestUnavailableDestinationIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close() // nothing listens any more
	r, err := New(withSecret(address))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Deliver(context.Background(), batch(t))
	if err == nil || report.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	assertNoSecret(t, err)
}

func TestNewRejectsUnusableURLs(t *testing.T) {
	for _, raw := range []string{
		"",
		"ftp://user:s3cret@example.com/",
		"https://",
		"/relative",
		"https://user:s3cret@example.com/#frag",
		"http://user:s3cret@[::1",
	} {
		_, err := New(raw)
		if !errors.Is(err, ErrURL) {
			t.Errorf("%q: err = %v", raw, err)
		}
		if err != nil && strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%q: error leaks the URL: %v", raw, err)
		}
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Duration{
		"5":                             5 * time.Second,
		"0":                             0,
		"Sat, 24 Oct 2026 09:00:30 GMT": 30 * time.Second,
		"Sat, 24 Oct 2026 08:00:00 GMT": 0,
	} {
		got, ok := retryAfter(value, now)
		if !ok || got != want {
			t.Errorf("%q = %v, %v; want %v", value, got, ok, want)
		}
	}
	for _, value := range []string{"", "-1", "soon"} {
		if _, ok := retryAfter(value, now); ok {
			t.Errorf("%q parsed", value)
		}
	}
}

// TestDispatcherRecoversFromUnavailableDestination runs the dispatcher and
// the HTTP adapter together against a destination that fails, then recovers.
func TestDispatcherRecoversFromUnavailableDestination(t *testing.T) {
	var requests atomic.Int32
	var mu sync.Mutex
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if requests.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	r, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	config := report.DefaultConfig()
	config.BackoffBase = time.Millisecond
	config.BackoffMax = 5 * time.Millisecond
	d, err := report.New(config, []report.Destination{{Name: "hook", Reporter: r}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Start()
	d.Enqueue("hook", report.Event{ID: "i:1"})
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := d.Stats()[0]
	if s.Delivered != 1 || s.Retries != 2 || requests.Load() != 3 {
		t.Fatalf("stats %+v after %d requests", s, requests.Load())
	}
	// Every attempt carries the same idempotency key, so a receiver can
	// collapse the duplicates a retry may cause.
	mu.Lock()
	defer mu.Unlock()
	if keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Fatalf("idempotency keys %v", keys)
	}
}
