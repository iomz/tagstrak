package tdt_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSubstrateHasNoApplicationDependencies enforces the extraction boundary:
// the identity and translation packages must depend on nothing but the
// standard library, so they can be lifted into a public module later without
// dragging application types along.
func TestSubstrateHasNoApplicationDependencies(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping dependency check in short mode")
	}
	const module = "github.com/iomz/tagstrak/v2"
	allowed := map[string]bool{
		module + "/internal/identity":    true,
		module + "/internal/tdt":         true,
		module + "/internal/tdt/gs1epc":  true,
		module + "/internal/tdt/iso":     true,
		module + "/internal/tdt/tdtcore": true,
		module + "/internal/tdt/tdtdef":  true,
	}
	for pkg := range allowed {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", pkg, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			if !strings.HasPrefix(dep, module) {
				continue // standard library
			}
			if !allowed[dep] {
				t.Errorf("%s depends on %s, which is outside the extractable substrate", pkg, dep)
			}
		}
	}
}
