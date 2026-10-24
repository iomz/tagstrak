package gosstrak

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configfile "github.com/iomz/tagstrak/v2/internal/config"
	"github.com/iomz/tagstrak/v2/internal/control"
	"github.com/iomz/tagstrak/v2/internal/inventory"
)

var controlToken = []byte("0123456789abcdef0123456789abcdef")

func spec(id, pattern string, destinations ...string) configfile.Subscription {
	return configfile.Subscription{ID: id, Patterns: []string{pattern}, Destinations: destinations}
}

func TestSubscriptionTableRevisions(t *testing.T) {
	tbl := NewSubscriptionTable([]string{"erp"})
	if tbl.Revision() != 0 {
		t.Fatal("new table is not at revision 0")
	}
	rev, err := tbl.Replace([]configfile.Subscription{spec("a", "urn:epc:idpat:sgtin:*.*.*", "erp")}, nil, nil)
	if err != nil || rev != 1 {
		t.Fatalf("rev %d, err %v", rev, err)
	}
	stale := uint64(0)
	if _, err := tbl.Replace(nil, &stale, nil); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("stale If-Match: %v", err)
	}
	// A vetoed commit publishes nothing.
	veto := errors.New("disk full")
	if _, err := tbl.Replace(nil, nil, func(uint64, []configfile.Subscription) error { return veto }); !errors.Is(err, veto) {
		t.Fatalf("veto: %v", err)
	}
	if s, r := tbl.Snapshot(); r != 1 || len(s) != 1 {
		t.Fatalf("veto changed state: %v at %d", s, r)
	}
	// Errors locate the field.
	var fe *FieldError
	_, err = tbl.Replace([]configfile.Subscription{spec("a", "urn:epc:idpat:sgtin:*.*.*", "crm")}, nil, nil)
	if !errors.As(err, &fe) || fe.Field != "subscriptions[0].destinations[0]" {
		t.Fatalf("unknown destination: %v", err)
	}
	_, err = tbl.Replace([]configfile.Subscription{spec("a", "urn:epc:idpat:sgtin:*.*.*"), spec("b", "urn:nope")}, nil, nil)
	if !errors.As(err, &fe) || fe.Field != "subscriptions[1].patterns[0]" {
		t.Fatalf("bad pattern: %v", err)
	}
	// Snapshots are copies.
	s, _ := tbl.Snapshot()
	s[0].Patterns[0] = "changed"
	if again, _ := tbl.Snapshot(); again[0].Patterns[0] == "changed" {
		t.Fatal("Snapshot exposed internal state")
	}
}

type apiFixture struct {
	app   *App
	table *SubscriptionTable
	state string
	logs  *bytes.Buffer
}

func newAPI(t *testing.T, source string) apiFixture {
	t.Helper()
	tbl := NewSubscriptionTable([]string{"erp"})
	if err := tbl.Load([]configfile.Subscription{spec("product", "urn:epc:idpat:sgtin:0614141.812345.*", "erp")}, 1); err != nil {
		t.Fatal(err)
	}
	var store *StateStore
	state := filepath.Join(t.TempDir(), "subscriptions.json")
	if source == configfile.SourceAPI {
		store = &StateStore{Path: state}
	}
	logs := &bytes.Buffer{}
	a, err := New(config(), dialFunc(func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("unused") }),
		consumerFunc(func(context.Context, inventory.Observation) error { return nil }), slog.New(slog.NewJSONHandler(logs, nil)),
		WithControl(ControlSettings{Subscriptions: tbl, Source: source, Store: store}))
	if err != nil {
		t.Fatal(err)
	}
	return apiFixture{app: a, table: tbl, state: state, logs: logs}
}

func (f apiFixture) do(t *testing.T, method, path, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.app.controlHandler().ServeHTTP(w, r)
	return w
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct{ Error control.Error }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body %s: %v", w.Body, err)
	}
	return body.Error.Code, body.Error.Field
}

var jsonHeader = map[string]string{"Content-Type": "application/json"}

func TestControlAPIReads(t *testing.T) {
	f := newAPI(t, configfile.SourceFile)
	if w := f.do(t, http.MethodGet, "/v1/status", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"queue_capacity"`) {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	w := f.do(t, http.MethodGet, "/v1/subscriptions", "", nil)
	var doc subscriptionDocument
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil || w.Code != 200 {
		t.Fatalf("subscriptions %d %s", w.Code, w.Body)
	}
	if doc.Source != "file" || doc.Revision != 1 || len(doc.Subscriptions) != 1 || w.Header().Get("ETag") != `"1"` {
		t.Fatalf("document %+v, ETag %s", doc, w.Header().Get("ETag"))
	}
}

func TestControlAPIFileModeRefusesWrites(t *testing.T) {
	f := newAPI(t, configfile.SourceFile)
	w := f.do(t, http.MethodPut, "/v1/subscriptions", `{"subscriptions":[]}`, jsonHeader)
	if code, _ := errorCode(t, w); w.Code != http.StatusConflict || code != "managed_by_file" {
		t.Fatalf("status %d code %s", w.Code, code)
	}
	if f.table.Revision() != 1 {
		t.Fatal("file-mode PUT changed subscriptions")
	}
	if !strings.Contains(f.logs.String(), `"outcome":"conflict"`) {
		t.Fatalf("conflict not audited: %s", f.logs)
	}
}

func TestControlAPIReplacesAndPersists(t *testing.T) {
	f := newAPI(t, configfile.SourceAPI)
	body := `{"subscriptions":[{"id":"company","patterns":["urn:epc:idpat:sgtin:0614141.*.*"],"destinations":["erp"]}]}`
	w := f.do(t, http.MethodPut, "/v1/subscriptions", body, map[string]string{"Content-Type": "application/json", "If-Match": `"1"`})
	if w.Code != 200 || w.Header().Get("ETag") != `"2"` {
		t.Fatalf("status %d ETag %s body %s", w.Code, w.Header().Get("ETag"), w.Body)
	}
	state, err := configfile.LoadState(f.state)
	if err != nil || state.Revision != 2 || len(state.Subscriptions) != 1 || state.Subscriptions[0].ID != "company" {
		t.Fatalf("state %+v, %v", state, err)
	}
	// The audit record carries revisions and a count, never patterns.
	if !strings.Contains(f.logs.String(), `"action":"subscriptions.replace","outcome":"ok"`) || strings.Contains(f.logs.String(), "0614141") {
		t.Fatalf("audit log: %s", f.logs)
	}
}

func TestControlAPIRejections(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		header     map[string]string
		status     int
		code       string
		field      string
	}{
		{"stale revision", `{"subscriptions":[]}`, map[string]string{"Content-Type": "application/json", "If-Match": `"9"`}, 412, "revision_mismatch", ""},
		{"bad If-Match", `{"subscriptions":[]}`, map[string]string{"Content-Type": "application/json", "If-Match": "soon"}, 400, "invalid_precondition", ""},
		{"bad pattern", `{"subscriptions":[{"id":"a","patterns":["urn:nope"]}]}`, jsonHeader, 400, "invalid_subscriptions", "subscriptions[0].patterns[0]"},
		{"unknown route", `{"subscriptions":[{"id":"a","patterns":["urn:epc:idpat:sgtin:*.*.*"],"destinations":["crm"]}]}`, jsonHeader, 400, "invalid_subscriptions", "subscriptions[0].destinations[0]"},
		{"unknown field", `{"subscription":[]}`, jsonHeader, 400, "invalid_json", ""},
		{"wrong content type", `{"subscriptions":[]}`, map[string]string{"Content-Type": "text/plain"}, 415, "unsupported_media_type", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t, configfile.SourceAPI)
			w := f.do(t, http.MethodPut, "/v1/subscriptions", tc.body, tc.header)
			code, field := errorCode(t, w)
			if w.Code != tc.status || code != tc.code || field != tc.field {
				t.Fatalf("status %d code %s field %q", w.Code, code, field)
			}
			if f.table.Revision() != 1 {
				t.Fatal("rejected PUT changed subscriptions")
			}
			if _, err := os.Stat(f.state); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected PUT wrote state")
			}
		})
	}
}

func TestControlAPIStateWriteFailureChangesNothing(t *testing.T) {
	f := newAPI(t, configfile.SourceAPI)
	f.app.control.Store.Path = filepath.Join(t.TempDir(), "missing", "dir", "s.json")
	w := f.do(t, http.MethodPut, "/v1/subscriptions", `{"subscriptions":[]}`, jsonHeader)
	if code, _ := errorCode(t, w); w.Code != 500 || code != "state_not_saved" {
		t.Fatalf("status %d code %s", w.Code, code)
	}
	if f.table.Revision() != 1 {
		t.Fatal("subscriptions changed although the state was not saved")
	}
}

// TestAppServesControlOverLoopbackTCP runs the App with a real TCP control
// listener: requests without the token are refused, with it they succeed.
func TestAppServesControlOverLoopbackTCP(t *testing.T) {
	c := config()
	c.Control = control.ListenConfig{Address: "127.0.0.1:0"}
	tbl := NewSubscriptionTable(nil)
	a, err := New(c, dialFunc(func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }),
		consumerFunc(func(context.Context, inventory.Observation) error { return nil }), nil,
		WithControl(ControlSettings{Token: controlToken, Subscriptions: tbl, Source: configfile.SourceFile}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	eventually(t, func() bool { return a.ControlAddress() != "" })
	url := "http://" + a.ControlAddress() + "/v1/status"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+string(controlToken))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status %d", resp.StatusCode)
	}
	cancel()
	if err := await(t, done); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	if _, err := net.Dial("tcp", a.ControlAddress()); err == nil {
		t.Fatal("control listener survived shutdown")
	}
}

func TestNewRefusesUnauthenticatedTCPControl(t *testing.T) {
	c := config()
	c.Control = control.ListenConfig{Address: "127.0.0.1:0"}
	tbl := NewSubscriptionTable(nil)
	for name, tokenValue := range map[string][]byte{"no token": nil, "short token": []byte("short")} {
		_, err := New(c, dialFunc(func(context.Context, string, string) (net.Conn, error) { return nil, nil }),
			consumerFunc(func(context.Context, inventory.Observation) error { return nil }), nil,
			WithControl(ControlSettings{Token: tokenValue, Subscriptions: tbl, Source: configfile.SourceFile}))
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c.Control = control.ListenConfig{Address: "0.0.0.0:9000"}
	if err := c.Validate(); err == nil {
		t.Fatal("non-loopback control address accepted")
	}
}

func TestReloadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gosstrak.toml")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tbl := NewSubscriptionTable(nil)
	if err := tbl.Load([]configfile.Subscription{spec("a", "urn:epc:idpat:sgtin:*.*.*")}, 1); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	write("version = 1\n[[subscriptions]]\nid = \"a\"\npatterns = [\"urn:epc:idpat:sgtin:*.*.*\"]\n[[subscriptions]]\nid = \"b\"\npatterns = [\"urn:epc:idpat:sscc:*.*\"]\n")
	if err := ReloadFile(context.Background(), path, tbl, logger); err != nil {
		t.Fatal(err)
	}
	if s, rev := tbl.Snapshot(); rev != 2 || len(s) != 2 {
		t.Fatalf("after reload %v at %d", s, rev)
	}
	for body, code := range map[string]string{
		"version = 1\n[[subscriptions]]\nid = \"a\"\npatterns = [\"urn:nope\"]\n":                 "invalid_subscriptions",
		"version = 1\n[reader]\nadress = \"x\"\n":                                                 "invalid_config",
		"version = 1\n[subscription_store]\nsource = \"api\"\nstate_file = \"/var/lib/s.json\"\n": "source_changed",
	} {
		logs.Reset()
		write(body)
		if err := ReloadFile(context.Background(), path, tbl, logger); err == nil {
			t.Errorf("%s: reload accepted", code)
		}
		if tbl.Revision() != 2 {
			t.Fatalf("%s: rejected reload changed subscriptions", code)
		}
		if !strings.Contains(logs.String(), `"actor":"signal"`) || !strings.Contains(logs.String(), `"error_code":"`+code+`"`) {
			t.Errorf("%s: audit %s", code, logs.String())
		}
	}
}

func TestPipelineLoadsAPIState(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "s.json")
	if err := configfile.WriteState(state, configfile.State{Revision: 5, Subscriptions: []configfile.Subscription{spec("a", "urn:epc:idpat:sgtin:*.*.*")}}); err != nil {
		t.Fatal(err)
	}
	source, path := configfile.SourceAPI, state
	f := configfile.File{Version: configfile.Version, Store: configfile.Store{Source: &source, StateFile: &path}}
	assembly, err := Pipeline(f, DefaultConfig(), noLookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	if assembly.Source != "api" || assembly.Subscriptions.Revision() != 5 {
		t.Fatalf("assembly source %s revision %d", assembly.Source, assembly.Subscriptions.Revision())
	}
	// A control token without a control listener is a configuration error.
	f.Control.Token = &configfile.SecretSource{Env: "T"}
	if _, err := Pipeline(f, DefaultConfig(), noLookup, nil); err == nil || !strings.Contains(err.Error(), "control.token is set") {
		t.Fatalf("err = %v", err)
	}
}
