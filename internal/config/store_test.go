package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestControlAndStoreValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"socket and address", "version = 1\n[control]\nsocket = \"/a\"\naddress = \"127.0.0.1:1\"\n", "not both"},
		{"inline token", "version = 1\n[control]\naddress = \"127.0.0.1:1\"\n[control.token]\nvalue = \"x\"\n", "control.token: inline secret values are not allowed"},
		{"unknown source", "version = 1\n[subscription_store]\nsource = \"db\"\n", `must be "file" or "api"`},
		{"api without state", "version = 1\n[subscription_store]\nsource = \"api\"\n", "state_file is required"},
		{"relative state", "version = 1\n[subscription_store]\nsource = \"api\"\nstate_file = \"state.json\"\n", "must be absolute"},
		{"state in file mode", "version = 1\n[subscription_store]\nstate_file = \"/var/lib/s.json\"\n", `applies only to source "api"`},
		{"api with subscriptions", "version = 1\n[subscription_store]\nsource = \"api\"\nstate_file = \"/var/lib/s.json\"\n[[subscriptions]]\nid = \"a\"\npatterns = [\"urn:epc:idpat:sgtin:*.*.*\"]\n", "must not be listed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(write(t, "c.toml", tc.body)); !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	f, err := Load(write(t, "c.toml", "version = 1\n[control]\naddress = \"127.0.0.1:9000\"\n[control.token]\nfile = \"/run/secrets/control\"\n[subscription_store]\nsource = \"api\"\nstate_file = \"/var/lib/gosstrak/subscriptions.json\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.SubscriptionSource() != SourceAPI || *f.Control.Address != "127.0.0.1:9000" || f.Control.Token.File != "/run/secrets/control" {
		t.Fatalf("file %+v", f)
	}
	if (File{}).SubscriptionSource() != SourceFile {
		t.Fatal("default source is not file")
	}
}

func TestStateRoundTripIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subscriptions.json")
	empty, err := LoadState(path)
	if err != nil || empty.Revision != 0 || len(empty.Subscriptions) != 0 {
		t.Fatalf("missing state: %+v, %v", empty, err)
	}
	want := State{Revision: 7, Subscriptions: []Subscription{{ID: "a", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}, Destinations: []string{"erp"}}}}
	if err := WriteState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	want.Version = Version
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %v, %v", info.Mode(), err)
	}
	// Rewriting leaves no temporary files behind.
	if err := WriteState(path, State{Revision: 8}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries after rewrite", len(entries))
	}
	if s, _ := LoadState(path); s.Revision != 8 || s.Subscriptions == nil {
		t.Fatalf("rewritten state %+v", s)
	}
}

func TestLoadStateRejectsDamage(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      "{",
		"unknown field": `{"version":1,"revision":1,"subscriptions":[],"extra":1}`,
		"wrong version": `{"version":2,"revision":1,"subscriptions":[]}`,
		"trailing":      `{"version":1,"revision":1,"subscriptions":[]} {}`,
		"no patterns":   `{"version":1,"revision":1,"subscriptions":[{"id":"a"}]}`,
	} {
		if _, err := LoadState(write(t, "s.json", body)); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
