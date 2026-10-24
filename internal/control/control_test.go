package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var token = []byte("0123456789abcdef0123456789abcdef")

func privateDir(t *testing.T) string {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit on macOS.
	dir, err := os.MkdirTemp("/tmp", "ctl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestListenConfigRefusesRemoteAddresses(t *testing.T) {
	for _, address := range []string{"0.0.0.0:9000", "[::]:9000", "10.0.0.5:9000", "example.com:9000", "192.168.1.2:9000", "9000", ""} {
		c := ListenConfig{Address: address}
		if address == "" {
			c = ListenConfig{Address: ":9000"}
		}
		if err := c.Validate(); !errors.Is(err, ErrConfig) {
			t.Errorf("%q accepted: %v", c.Address, err)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "127.0.0.2:9000", "[::1]:9000", "localhost:9000"} {
		if err := (ListenConfig{Address: address}).Validate(); err != nil {
			t.Errorf("%q rejected: %v", address, err)
		}
	}
	if err := (ListenConfig{Socket: "/a.sock", Address: "127.0.0.1:1"}).Validate(); !errors.Is(err, ErrConfig) {
		t.Errorf("both accepted: %v", err)
	}
	if err := (ListenConfig{Socket: "relative.sock"}).Validate(); !errors.Is(err, ErrConfig) {
		t.Errorf("relative socket accepted: %v", err)
	}
}

func TestUnixSocketRules(t *testing.T) {
	open := t.TempDir()
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(ListenConfig{Socket: filepath.Join(open, "c.sock")}); !errors.Is(err, ErrConfig) {
		t.Fatalf("non-private parent accepted: %v", err)
	}
	dir := privateDir(t)
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(ListenConfig{Socket: existing}); err == nil {
		t.Fatal("existing path replaced")
	}
	if data, _ := os.ReadFile(existing); string(data) != "keep" {
		t.Fatal("existing path changed")
	}
	path := filepath.Join(dir, "c.sock")
	l, transport, err := Listen(ListenConfig{Socket: path})
	if err != nil {
		t.Fatal(err)
	}
	if transport != TransportUnix {
		t.Fatalf("transport %d", transport)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v, %v", info.Mode(), err)
	}
	_ = l.Close()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket survived close")
	}
}

func TestNewRequiresStrongTokenForTCP(t *testing.T) {
	l, transport, err := Listen(ListenConfig{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err := New(l, transport, http.NotFoundHandler(), ServerConfig{}); !errors.Is(err, ErrConfig) {
		t.Fatalf("TCP without token: %v", err)
	}
	if _, err := New(l, transport, http.NotFoundHandler(), ServerConfig{Token: []byte("short")}); !errors.Is(err, ErrConfig) {
		t.Fatalf("short token: %v", err)
	}
	if _, err := New(l, transport, http.NotFoundHandler(), ServerConfig{Token: token, MaxConns: 5000}); !errors.Is(err, ErrConfig) {
		t.Fatalf("unbounded connections: %v", err)
	}
	s, err := New(l, transport, http.NotFoundHandler(), ServerConfig{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	// Every deadline and header bound #10 requires is set.
	h := s.http
	if h.ReadHeaderTimeout != 2*time.Second || h.ReadTimeout != 5*time.Second || h.WriteTimeout != 5*time.Second ||
		h.IdleTimeout != 30*time.Second || h.MaxHeaderBytes != 8<<10 {
		t.Fatalf("server bounds %+v", h)
	}
}

// serve runs a TCP control server around handler and returns its base URL.
func serve(t *testing.T, handler http.Handler, logger *slog.Logger, maxConns int) string {
	t.Helper()
	l, transport, err := Listen(ListenConfig{Address: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(l, transport, handler, ServerConfig{Token: token, Logger: logger, MaxConns: maxConns})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		if err := <-done; !IsClosed(err) {
			t.Errorf("serve: %v", err)
		}
	})
	return "http://" + s.Addr().String()
}

// TestAuthenticationFailsClosed checks that no request without the exact
// token reaches the handler, its body is never read, and the failure is
// audited without the presented credential.
func TestAuthenticationFailsClosed(t *testing.T) {
	var reached atomic.Int32
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	base := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		WriteJSON(w, http.StatusOK, map[string]string{"actor": Actor(r), "id": RequestID(r)})
	}), logger, 0)

	for name, header := range map[string]string{
		"missing":      "",
		"wrong token":  "Bearer " + strings.Repeat("x", len(token)),
		"prefix only":  "Bearer ",
		"basic scheme": "Basic " + string(token),
		"token prefix": "Bearer " + string(token[:len(token)-1]),
	} {
		req, _ := http.NewRequest(http.MethodPut, base+"/x", strings.NewReader(`{"secret":"payload-s3cret"}`))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Error Error }
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized || body.Error.Code != "unauthenticated" || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: status %d, body %+v", name, resp.StatusCode, body)
		}
	}
	if reached.Load() != 0 {
		t.Fatalf("handler reached %d times without valid credentials", reached.Load())
	}
	if n := strings.Count(logs.String(), `"action":"auth"`); n != 5 {
		t.Fatalf("%d auth audit records, want 5:\n%s", n, logs.String())
	}
	for _, leaked := range []string{"payload-s3cret", strings.Repeat("x", len(token)), string(token)} {
		if strings.Contains(logs.String(), leaked) {
			t.Fatalf("audit log contains %q", leaked)
		}
	}

	// The right token, in any case for the scheme, reaches the handler.
	req, _ := http.NewRequest(http.MethodGet, base+"/x", nil)
	req.Header.Set("Authorization", "bearer "+string(token))
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("Forwarded", "for=203.0.113.9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || reached.Load() != 1 {
		t.Fatalf("authorized request: status %d", resp.StatusCode)
	}
	// Forwarded headers are ignored: the actor is the transport peer.
	if !strings.HasPrefix(got["actor"], "127.0.0.1:") || got["id"] == "" || resp.Header.Get("X-Request-ID") != got["id"] {
		t.Fatalf("actor %q, request ID %q / %q", got["actor"], got["id"], resp.Header.Get("X-Request-ID"))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("control responses must not be cached")
	}
}

func TestConnectionCap(t *testing.T) {
	base := serve(t, http.NotFoundHandler(), nil, 1)
	address := strings.TrimPrefix(base, "http://")
	first, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// Make sure the first connection has been accepted before the second.
	if _, err := first.Write([]byte("GET /x HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + string(token) + "\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 12)
	if _, err := io.ReadFull(first, buf); err != nil {
		t.Fatal(err)
	}
	second, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection beyond the cap was served")
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("connection beyond the cap was held open instead of closed")
	}
}

func TestReadJSON(t *testing.T) {
	type doc struct {
		Name string `json:"name"`
	}
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		code                    string
	}{
		{"ok", "application/json", `{"name":"a"}`, 0, ""},
		{"charset", "application/json; charset=utf-8", `{"name":"a"}`, 0, ""},
		{"wrong type", "text/plain", `{"name":"a"}`, 415, "unsupported_media_type"},
		{"no type", "", `{"name":"a"}`, 415, "unsupported_media_type"},
		{"too large", "application/json", `{"name":"` + strings.Repeat("a", 100) + `"}`, 413, "body_too_large"},
		{"unknown field", "application/json", `{"injected-name":"a"}`, 400, "invalid_json"},
		{"trailing", "application/json", `{"name":"a"} {}`, 400, "invalid_json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(tc.body))
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			w := httptest.NewRecorder()
			var v doc
			ok := ReadJSON(w, r, 64, &v)
			if tc.status == 0 {
				if !ok || v.Name != "a" {
					t.Fatalf("ok=%v value %+v", ok, v)
				}
				return
			}
			var body struct{ Error Error }
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if ok || w.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("ok=%v status %d code %q", ok, w.Code, body.Error.Code)
			}
			if strings.Contains(w.Body.String(), "injected-name") {
				t.Fatalf("error echoes request content: %s", w.Body)
			}
		})
	}
}

func TestAuditRecordShape(t *testing.T) {
	var logs bytes.Buffer
	AuditRecord(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)), "signal", "", "subscriptions.reload", "ok", slog.Int("count", 2))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"event": "control", "action": "subscriptions.reload", "outcome": "ok", "actor": "signal", "count": float64(2)} {
		if record[key] != want {
			t.Errorf("%s = %v, want %v", key, record[key], want)
		}
	}
	if _, ok := record["request_id"]; ok {
		t.Error("empty request ID recorded")
	}
}
