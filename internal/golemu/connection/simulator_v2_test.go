package connection

import (
	"path/filepath"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/inventory"
)

func TestSimulatorLoadsVersionedInventoryCycles(t *testing.T) {
	directory := t.TempDir()
	tag, err := inventory.ParseTag("3000")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "cycle-001.json")
	if err := inventory.SaveFile(path, []inventory.Tag{tag}, inventory.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	simulator := NewSimulator("127.0.0.1", 5084, 1500, 1000, directory, 1000)
	files, err := simulator.loadSimulationFiles()
	if err != nil {
		t.Fatal(err)
	}
	eventCycle := 0
	tags, err := simulator.loadTagsForNextEventCycle(files, &eventCycle)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Hex() != "3000" {
		t.Fatalf("tags = %#v", tags)
	}
}
