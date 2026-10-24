package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestFormatsDecodeIdentically keeps TOML and JSON one schema: the fixtures
// describe the same configuration and must decode to the same value.
func TestFormatsDecodeIdentically(t *testing.T) {
	fromTOML, err := Load(filepath.Join("testdata", "full.toml"))
	if err != nil {
		t.Fatal(err)
	}
	fromJSON, err := Load(filepath.Join("testdata", "full.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromTOML, fromJSON) {
		t.Fatalf("formats differ:\nTOML %+v\nJSON %+v", fromTOML, fromJSON)
	}
	f := fromTOML
	if *f.Reader.Address != "10.0.0.5:5084" || time.Duration(*f.Reader.ConnectTimeout) != 3*time.Second {
		t.Fatalf("reader %+v", f.Reader)
	}
	// An explicit zero is kept, distinct from an omitted field.
	if f.Reader.MaxRetries == nil || *f.Reader.MaxRetries != 0 || f.Reader.RetryMax != nil {
		t.Fatalf("pointer semantics lost: %+v", f.Reader)
	}
	if len(f.Subscriptions) != 2 || f.Subscriptions[1].Destinations != nil {
		t.Fatalf("subscriptions %+v", f.Subscriptions)
	}
	if h := f.Destinations[0].Headers["Authorization"]; h.Env != "ERP_TOKEN" || h.Prefix != "Bearer " {
		t.Fatalf("header source %+v", h)
	}
}

func TestLoadRejectsMalformedFiles(t *testing.T) {
	for _, tc := range []struct {
		name, file, body, want string
	}{
		{"unknown TOML key", "c.toml", "version = 1\n[reader]\nadress = \"x\"\n", "unknown keys: reader.adress"},
		{"unknown JSON key", "c.json", `{"version":1,"reader":{"adress":"x"}}`, "unknown field"},
		{"trailing JSON", "c.json", `{"version":1} {"version":1}`, "trailing data"},
		{"missing version", "c.toml", "[reader]\n", "version 0 is not supported"},
		{"future version", "c.json", `{"version":2}`, "version 2 is not supported"},
		{"yaml", "c.yaml", "version: 1\n", `unsupported format ".yaml"`},
		{"bad duration", "c.toml", "version = 1\n[reader]\nread_timeout = \"soon\"\n", "duration"},
		{"inline secret", "c.toml", "version = 1\n[[destinations]]\nname = \"a\"\nurl = \"https://a\"\n[destinations.headers.Authorization]\nvalue = \"s3cret\"\n", "inline secret values are not allowed"},
		{"two sources", "c.json", `{"version":1,"destinations":[{"name":"a","url":"https://a","headers":{"X":{"env":"A","file":"/b"}}}]}`, "not both"},
		{"subscription without patterns", "c.toml", "version = 1\n[[subscriptions]]\nid = \"a\"\n", "subscriptions[0].patterns is required"},
		{"destination without url", "c.json", `{"version":1,"destinations":[{"name":"a"}]}`, "destinations[0].url is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.file, tc.body))
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(fmt.Sprint(err), "s3cret") {
				t.Fatalf("error echoes a secret: %v", err)
			}
		})
	}
}

func TestLoadRejectsOversizedFile(t *testing.T) {
	body := "version = 1\n#" + strings.Repeat("x", MaxFileBytes) + "\n"
	if _, err := Load(write(t, "big.toml", body)); !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretIsRedactedEverywhere(t *testing.T) {
	s, err := SecretSource{Env: "TOKEN", Prefix: "Bearer "}.Resolve(func(string) (string, bool) { return "s3cret", true })
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Reveal()) != "Bearer s3cret" {
		t.Fatalf("Reveal = %q", s.Reveal())
	}
	j, _ := json.Marshal(struct{ S Secret }{s})
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "secret", s)
	for _, rendered := range []string{fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s", s, s, s, s), string(j), logs.String()} {
		if strings.Contains(rendered, "s3cret") || !strings.Contains(rendered, "[redacted]") {
			t.Fatalf("secret leaked or not redacted: %s", rendered)
		}
	}
	// Reveal returns a copy, so callers cannot mutate the held value.
	r := s.Reveal()
	r[0] = 'X'
	if string(s.Reveal()) != "Bearer s3cret" {
		t.Fatal("Reveal exposed internal storage")
	}
}

func TestSecretSourcesFailClosed(t *testing.T) {
	empty := func(string) (string, bool) { return "", true }
	unset := func(string) (string, bool) { return "", false }
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.WriteFile(private, []byte("tok3n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := (SecretSource{File: private}).Resolve(unset); err != nil || string(s.Reveal()) != "tok3n" {
		t.Fatalf("private file: %q, %v", s.Reveal(), err)
	}
	readable := filepath.Join(dir, "readable")
	if err := os.WriteFile(readable, []byte("tok3n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blank := filepath.Join(dir, "blank")
	if err := os.WriteFile(blank, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), MaxSecretBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		source SecretSource
		lookup func(string) (string, bool)
	}{
		"unset env":      {SecretSource{Env: "TOKEN"}, unset},
		"empty env":      {SecretSource{Env: "TOKEN"}, empty},
		"missing file":   {SecretSource{File: filepath.Join(dir, "absent")}, unset},
		"directory":      {SecretSource{File: dir}, unset},
		"empty file":     {SecretSource{File: blank}, unset},
		"oversized file": {SecretSource{File: big}, unset},
		"no source":      {SecretSource{Prefix: "Bearer "}, unset},
	}
	if runtime.GOOS != "windows" {
		cases["readable file"] = struct {
			source SecretSource
			lookup func(string) (string, bool)
		}{SecretSource{File: readable}, unset}
	}
	for name, tc := range cases {
		if _, err := tc.source.Resolve(tc.lookup); !errors.Is(err, ErrSecret) {
			t.Errorf("%s: err = %v", name, err)
		} else if strings.Contains(err.Error(), "tok3n") {
			t.Errorf("%s: error echoes the secret: %v", name, err)
		}
	}
}
