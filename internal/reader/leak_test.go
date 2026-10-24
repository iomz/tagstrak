package reader

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package's tests if any goroutine outlives them, so every
// Run, server, worker and watcher must be joined before it returns (#11).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
