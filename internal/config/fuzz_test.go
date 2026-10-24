package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configSeeds are shared by both formats: valid fixtures, malformed and
// hostile inputs, and documents exactly at and one byte over the file limit.
func configSeeds(f *testing.F, ext string) {
	f.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "full"+ext))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(body)
	pad := func(prefix, comment string, size int) []byte {
		return []byte(prefix + comment + strings.Repeat("x", size-len(prefix)-len(comment)))
	}
	if ext == ".toml" {
		f.Add([]byte("version = 1\n"))
		f.Add([]byte("version = 1\n[[subscriptions]]\nid = \"a\"\npatterns = [\"urn:epc:idpat:sgtin:*.*.*\"]\n"))
		f.Add([]byte("version = 1\n[destinations.headers.X]\nvalue = \"inline\"\n"))
		f.Add([]byte("version = 1\n[reader]\nmax_retries = -9223372036854775808\nread_timeout = \"-1h\"\n"))
		f.Add([]byte("[[a]]\n[[a.b]]\n[[a.b.c]]\n"))
		f.Add(pad("version = 1\n", "#", MaxFileBytes))
		f.Add(pad("version = 1\n", "#", MaxFileBytes+1))
	} else {
		f.Add([]byte(`{"version":1}`))
		f.Add([]byte(`{"version":1,"subscriptions":[{"id":"a","patterns":[]}]}`))
		f.Add([]byte(`{"version":1} {"version":1}`))
		f.Add([]byte(`{"version":1,"reader":{"max_frame_bytes":4294967296}}`))
		f.Add([]byte(strings.Repeat("[", 10000)))
		f.Add(pad(`{"version":1,"reader":{"source":"`, "", MaxFileBytes-3))
	}
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xfe, 0x00})
}

// checkDecoded asserts what a successfully decoded file guarantees.
func checkDecoded(t *testing.T, f File) {
	t.Helper()
	if f.Version != Version {
		t.Fatalf("accepted version %d", f.Version)
	}
	for i, s := range f.Subscriptions {
		if s.ID == "" || len(s.Patterns) == 0 {
			t.Fatalf("accepted incomplete subscription %d: %+v", i, s)
		}
	}
	for i, d := range f.Destinations {
		if d.Name == "" || d.URL == "" {
			t.Fatalf("accepted incomplete destination %d: %+v", i, d)
		}
		for name, source := range d.Headers {
			if source.Value != nil || (source.Env == "") == (source.File == "") {
				t.Fatalf("accepted invalid secret source %s: %+v", name, source)
			}
		}
	}
}

func FuzzDecodeTOML(f *testing.F) {
	configSeeds(f, ".toml")
	f.Fuzz(func(t *testing.T, body []byte) {
		if file, err := decode(".toml", body); err == nil {
			checkDecoded(t, file)
		}
	})
}

func FuzzDecodeJSON(f *testing.F) {
	configSeeds(f, ".json")
	f.Fuzz(func(t *testing.T, body []byte) {
		if file, err := decode(".json", body); err == nil {
			checkDecoded(t, file)
		}
	})
}

func FuzzDecodeState(f *testing.F) {
	f.Add([]byte(`{"version":1,"revision":3,"subscriptions":[{"id":"a","patterns":["urn:epc:idpat:sgtin:*.*.*"]}]}`))
	f.Add([]byte(`{"version":1,"revision":18446744073709551615,"subscriptions":[]}`))
	f.Add([]byte(`{"version":1,"revision":-1,"subscriptions":[]}`))
	f.Add([]byte(`{"version":1,"subscriptions":null}`))
	f.Add([]byte(`{"version":1,"revision":1,"subscriptions":[]} []`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, body []byte) {
		s, err := decodeState(body)
		if err != nil {
			return
		}
		if s.Version != Version {
			t.Fatalf("accepted version %d", s.Version)
		}
		for i, sub := range s.Subscriptions {
			if sub.ID == "" || len(sub.Patterns) == 0 {
				t.Fatalf("accepted incomplete subscription %d", i)
			}
		}
	})
}

// TestLoadAcceptsExactlyTheFileLimit pins the boundary the fuzz seeds probe:
// a file of exactly MaxFileBytes loads, one byte more does not.
func TestLoadAcceptsExactlyTheFileLimit(t *testing.T) {
	prefix := "version = 1\n#"
	at := prefix + strings.Repeat("x", MaxFileBytes-len(prefix))
	if _, err := Load(write(t, "at.toml", at)); err != nil {
		t.Fatalf("file at the limit rejected: %v", err)
	}
	if _, err := Load(write(t, "over.toml", at+"x")); err == nil {
		t.Fatal("file over the limit accepted")
	}
}
