package control

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fuzzBodyLimit = 64

func FuzzReadJSON(f *testing.F) {
	f.Add(`{"name":"a"}`, "application/json")
	f.Add(`{"name":"a"}`, "application/json; charset=utf-8")
	f.Add(`{"name":"a"}`, "text/plain")
	f.Add(`{"name":"a"}`, "application/json; =")
	f.Add(`{"nam":"a"}`, "application/json")
	f.Add(`{"name":"a"} {}`, "application/json")
	f.Add(`{"name":"`+strings.Repeat("a", fuzzBodyLimit-12)+`"}`, "application/json")
	f.Add(`{"name":"`+strings.Repeat("a", fuzzBodyLimit-11)+`"}`, "application/json")
	f.Fuzz(func(t *testing.T, body, contentType string) {
		r := httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		var v struct {
			Name string `json:"name"`
		}
		ok := ReadJSON(w, r, fuzzBodyLimit, &v)
		switch {
		case ok && len(body) > fuzzBodyLimit:
			t.Fatalf("accepted a %d-byte body over the limit", len(body))
		case ok && w.Code != http.StatusOK:
			t.Fatalf("accepted but wrote status %d", w.Code)
		case !ok && w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusUnsupportedMediaType:
			t.Fatalf("rejected with unexpected status %d", w.Code)
		case !ok && len(body) >= 16 && bytes.Contains(w.Body.Bytes(), []byte(body)):
			// Short bodies can coincide with words of the fixed messages.
			t.Fatalf("error response echoes the request body")
		}
	})
}
