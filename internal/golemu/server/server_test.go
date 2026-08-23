package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/inventory"
)

func TestNewServerUsesSharedInventory(t *testing.T) {
	server := NewServer("127.0.0.1", 5084, 3000, 1500, 10000, 5, 1000, "tags.json")
	if server.inventory == nil || server.llrpHandler == nil || server.isConnAlive == nil {
		t.Fatal("server dependencies were not initialized")
	}
	if server.file != "tags.json" {
		t.Fatalf("inventory file = %q", server.file)
	}
}

func TestLoadInventory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.json")
	tag, err := inventory.ParseTag("3000")
	if err != nil {
		t.Fatal(err)
	}
	if err := inventory.SaveFile(path, []inventory.Tag{tag}, inventory.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	server := NewServer("127.0.0.1", 5084, 3000, 1500, 10000, 5, 1000, path)
	server.loadInventory()
	if tags := server.inventory.Snapshot(); len(tags) != 1 || tags[0].Hex() != "3000" {
		t.Fatalf("loaded tags = %#v", tags)
	}
}

func TestLoadInventoryRejectsMalformedDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"tags":["bad"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := NewServer("127.0.0.1", 5084, 3000, 1500, 10000, 5, 1000, path)
	server.loadInventory()
	if tags := server.inventory.Snapshot(); len(tags) != 0 {
		t.Fatalf("malformed document loaded tags = %#v", tags)
	}
}
