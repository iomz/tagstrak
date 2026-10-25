package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestParseWorkflows(t *testing.T) {
	for _, args := range [][]string{{"server", "--inventory", "../../examples/rig/inventory.json"}, {"simulator", "--scenario", "../../examples/rig/scenario.json"}} {
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
		{"client"}, {"server"}, {"simulator", "--scenario", path}, {"server", "--inventory", "missing"},
		{"server", "--inventory", "../../examples/rig/inventory.json", "--max-clients", "0"},
		{"server", "--inventory", "../../examples/rig/inventory.json", "--report-interval", "0"},
		{"server", "--inventory", "../../examples/rig/inventory.json", "--frame-bytes", "4294967296"},
		{"simulator", "--scenario", t.TempDir()},
	} {
		if _, _, err := parse(args, io.Discard); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

// TestFlagsAreDocumented checks that every emulator and uii flag appears in
// docs/rig.md.
func TestFlagsAreDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/rig.md")
	if err != nil {
		t.Fatal(err)
	}
	var help bytes.Buffer
	for _, args := range [][]string{{"server", "--help"}, {"simulator", "--help"}} {
		_, _, _ = parse(args, &help)
	}
	for _, kind := range []string{"epc", "iso17360", "iso17363"} {
		_ = uii([]string{kind, "--help"}, io.Discard, &help)
	}
	for _, name := range regexp.MustCompile(`(?m)^  -([a-z-]+)`).FindAllStringSubmatch(help.String(), -1) {
		if !regexp.MustCompile(`--` + name[1] + `([^a-z-]|$)`).Match(doc) {
			t.Errorf("--%s is not documented in docs/rig.md", name[1])
		}
	}
}
