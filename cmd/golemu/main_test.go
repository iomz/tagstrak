package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParseWorkflows(t *testing.T) {
	for _, args := range [][]string{{"server", "--inventory", "../../examples/golemu/inventory.json"}, {"simulator", "--scenario", "../../examples/golemu/scenario.json"}} {
		c, s, err := parse(args, io.Discard)
		if err != nil || s == nil || c.Emulator.MaxClients != 8 {
			t.Fatal(c, s, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err = run(ctx, args, io.Discard); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
}
func TestInvalidInputsBeforeRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"seed":0,"cycles":[["invalid"]]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"client"}, {"server"}, {"simulator", "--scenario", path}, {"server", "--inventory", "missing"},
		{"server", "--inventory", "../../examples/golemu/inventory.json", "--max-clients", "0"},
		{"server", "--inventory", "../../examples/golemu/inventory.json", "--report-interval", "0"},
		{"server", "--inventory", "../../examples/golemu/inventory.json", "--frame-bytes", "4294967296"},
		{"simulator", "--scenario", t.TempDir()},
	} {
		if _, _, err := parse(args, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
