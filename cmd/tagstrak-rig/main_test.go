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
	for _, args := range [][]string{{"reader", "--inventory", "../../examples/rig/inventory.json", "--shuffle", "--seed", "7"}, {"reader", "--scenario", "../../examples/rig/scenario.json"}} {
		c, s, err := parse(args, io.Discard)
		if err != nil || s == nil || c.Emulator.MaxClients != 8 {
			t.Fatal(c, s, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err = run(ctx, args, io.Discard, io.Discard); !errors.Is(err, context.Canceled) {
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
		{"client"}, {"reader"}, {"reader", "--scenario", path}, {"reader", "--inventory", "missing"},
		{"server", "--inventory", "../../examples/rig/inventory.json"},
		{"simulator", "--scenario", "../../examples/rig/scenario.json"},
		{"reader", "--scenario", "../../examples/rig/scenario.json", "--inventory", "../../examples/rig/inventory.json"},
		{"reader", "--scenario", "../../examples/rig/scenario.json", "--seed", "1"},
		{"reader", "--scenario", "../../examples/rig/scenario.json", "--shuffle"},
		{"reader", "--scenario", "../../examples/rig/scenario.json", "extra"},
		{"reader", "--scenario", "", "--inventory", "../../examples/rig/inventory.json"},
		{"reader", "--inventory", ""},
		{"reader", "--inventory", "../../examples/rig/inventory.json", "--max-clients", "0"},
		{"reader", "--inventory", "../../examples/rig/inventory.json", "--report-interval", "0"},
		{"reader", "--inventory", "../../examples/rig/inventory.json", "--frame-bytes", "4294967296"},
		{"reader", "--scenario", t.TempDir()},
	} {
		if _, _, err := parse(args, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}
