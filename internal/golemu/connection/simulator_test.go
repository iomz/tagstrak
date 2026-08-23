//
// Use of this source code is governed by The MIT License
// that can be found in the LICENSE file.

package connection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/inventory"
)

func TestNewSimulator(t *testing.T) {
	sim := NewSimulator("127.0.0.1", 5084, 1500, 10000, "/tmp/sim", 1000)

	if sim == nil {
		t.Fatal("NewSimulator returned nil")
	}
	if sim.ip != "127.0.0.1" {
		t.Errorf("expected ip 127.0.0.1, got %s", sim.ip)
	}
	if sim.port != 5084 {
		t.Errorf("expected port 5084, got %d", sim.port)
	}
	if sim.pdu != 1500 {
		t.Errorf("expected pdu 1500, got %d", sim.pdu)
	}
	if sim.reportInterval != 10000 {
		t.Errorf("expected reportInterval 10000, got %d", sim.reportInterval)
	}
	if sim.simulationDir != "/tmp/sim" {
		t.Errorf("expected simulationDir /tmp/sim, got %s", sim.simulationDir)
	}
	if sim.currentMessageID == nil {
		t.Error("currentMessageID should not be nil")
	}
	if *sim.currentMessageID != 1000 {
		t.Errorf("expected currentMessageID 1000, got %d", *sim.currentMessageID)
	}
	if sim.loopStarted == nil {
		t.Error("loopStarted should not be nil")
	}
}

func TestSimulatorNextMessageID(t *testing.T) {
	sim := NewSimulator("127.0.0.1", 5084, 1500, 10000, "/tmp/sim", 1000)
	if got := sim.nextMessageID(); got != 1000 {
		t.Fatalf("first message ID = %d, want 1000", got)
	}
	if got := sim.nextMessageID(); got != 1001 {
		t.Fatalf("second message ID = %d, want 1001", got)
	}
}

func TestSimulator_loadSimulationFiles(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "cycle1.json")
	file2 := filepath.Join(tmpDir, "cycle2.json")
	file3 := filepath.Join(tmpDir, "notinventory.txt")

	err := inventory.SaveFile(file1, nil, inventory.DefaultLimits())
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}
	err = inventory.SaveFile(file2, nil, inventory.DefaultLimits())
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	err = os.WriteFile(file3, []byte("test"), 0644)
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	sim := NewSimulator("127.0.0.1", 5084, 1500, 10000, tmpDir, 1000)

	files, err := sim.loadSimulationFiles()
	if err != nil {
		t.Fatalf("loadSimulationFiles failed: %v", err)
	}

	if len(files) != 2 {
		t.Errorf("expected 2 .json files, got %d", len(files))
	}

	// Verify files are sorted/ordered correctly
	found1 := false
	found2 := false
	for _, f := range files {
		if filepath.Base(f) == "cycle1.json" {
			found1 = true
		}
		if filepath.Base(f) == "cycle2.json" {
			found2 = true
		}
		if filepath.Base(f) == "notinventory.txt" {
			t.Error("should not include non-.json files")
		}
	}
	if !found1 || !found2 {
		t.Error("expected to find both cycle files")
	}
}

func TestSimulator_loadSimulationFiles_NoFiles(t *testing.T) {
	tmpDir := t.TempDir()

	sim := NewSimulator("127.0.0.1", 5084, 1500, 10000, tmpDir, 1000)

	_, err := sim.loadSimulationFiles()
	if err == nil {
		t.Error("expected error when no .json files found")
	}
}

func TestSimulator_loadSimulationFiles_InvalidDir(t *testing.T) {
	sim := NewSimulator("127.0.0.1", 5084, 1500, 10000, "/nonexistent/directory", 1000)

	_, err := sim.loadSimulationFiles()
	if err == nil {
		t.Error("expected error for nonexistent directory")
	}
}

func TestSimulator_loadTagsForNextEventCycle(t *testing.T) {
	tmpDir := t.TempDir()

	tag1, err := inventory.ParseTag("3000")
	if err != nil {
		t.Fatalf("failed to create tag1: %v", err)
	}

	// Create test files
	file1 := filepath.Join(tmpDir, "cycle1.json")
	file2 := filepath.Join(tmpDir, "cycle2.json")

	err = inventory.SaveFile(file1, []inventory.Tag{tag1}, inventory.DefaultLimits())
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create a second tag for the second cycle
	tag2, err := inventory.ParseTag("3002")
	if err != nil {
		t.Fatalf("failed to create tag2: %v", err)
	}
	err = inventory.SaveFile(file2, []inventory.Tag{tag2}, inventory.DefaultLimits())
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Verify files exist and have content
	info1, err := os.Stat(file1)
	if err != nil {
		t.Fatalf("file1 does not exist: %v", err)
	}
	if info1.Size() == 0 {
		t.Fatalf("file1 is empty")
	}
	info2, err := os.Stat(file2)
	if err != nil {
		t.Fatalf("file2 does not exist: %v", err)
	}
	if info2.Size() == 0 {
		t.Fatalf("file2 is empty")
	}

	sim := NewSimulator("127.0.0.1", 5084, 1500, 10000, tmpDir, 1000)

	simulationFiles := []string{file1, file2}
	eventCycle := 0

	tags, err := sim.loadTagsForNextEventCycle(simulationFiles, &eventCycle)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 {
		t.Errorf("expected 1 tag in cycle 0, got %d", len(tags))
	}
	if eventCycle != 0 {
		t.Errorf("expected loader not to advance eventCycle, got %d", eventCycle)
	}

	// Load second cycle
	eventCycle = 1
	tags, err = sim.loadTagsForNextEventCycle(simulationFiles, &eventCycle)
	if err != nil {
		t.Fatalf("loadTagsForNextEventCycle failed: %v", err)
	}
	if len(tags) != 1 {
		t.Errorf("expected 1 tag in cycle 1, got %d", len(tags))
	}
	if eventCycle != 1 {
		t.Errorf("expected loader not to advance eventCycle, got %d", eventCycle)
	}

	// Should wrap around
	eventCycle = 2
	tags, err = sim.loadTagsForNextEventCycle(simulationFiles, &eventCycle)
	if err != nil {
		t.Fatalf("loadTagsForNextEventCycle failed: %v", err)
	}
	if len(tags) != 1 {
		t.Errorf("expected 1 tag after wrap around, got %d", len(tags))
	}
	if eventCycle != 0 {
		t.Errorf("expected eventCycle to wrap to 0, got %d", eventCycle)
	}
}

func TestSimulator_loadTagsForNextEventCycle_InvalidFile(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "cycle1.json")
	err := os.WriteFile(file1, []byte("invalid inventory data"), 0644)
	if err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	sim := NewSimulator("127.0.0.1", 5084, 1500, 10000, tmpDir, 1000)

	simulationFiles := []string{file1}
	eventCycle := 0

	_, err = sim.loadTagsForNextEventCycle(simulationFiles, &eventCycle)
	if err == nil {
		t.Error("expected error when file is invalid")
	}
}
