//go:build unix

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	app "github.com/iomz/tagstrak/v2/internal/app/tagstrak"
	configfile "github.com/iomz/tagstrak/v2/internal/config"
)

// TestHangupReloadsSubscriptions sends a real SIGHUP. reloadOnHangup
// registers the handler before returning, so the signal cannot terminate the
// test process.
func TestHangupReloadsSubscriptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tagstrak.toml")
	if err := os.WriteFile(path, []byte("version = 1\n[[subscriptions]]\nid = \"a\"\npatterns = [\"urn:epc:idpat:sgtin:*.*.*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table := app.NewSubscriptionTable(nil)
	if err := table.Load([]configfile.Subscription{{ID: "a", Patterns: []string{"urn:epc:idpat:sgtin:*.*.*"}}}, 1); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	stop := reloadOnHangup(context.Background(), path, table, slog.New(slog.NewJSONHandler(&logs, nil)))
	defer stop()
	if err := os.WriteFile(path, []byte("version = 1\n[[subscriptions]]\nid = \"b\"\npatterns = [\"urn:epc:idpat:sscc:*.*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for table.Revision() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("SIGHUP did not reload; logs: %s", logs.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if spec, _ := table.Snapshot(); len(spec) != 1 || spec[0].ID != "b" {
		t.Fatalf("reloaded %v", spec)
	}
}
