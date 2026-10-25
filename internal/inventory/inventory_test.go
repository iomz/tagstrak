package inventory

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustTag(t *testing.T, hex string) Tag {
	t.Helper()
	tag, err := ParseTag(hex)
	if err != nil {
		t.Fatal(err)
	}
	return tag
}

func TestNewTagValidatesAndCopiesEPC(t *testing.T) {
	if _, err := NewTag([]byte{0x30}); !errors.Is(err, ErrInvalidEPCLength) {
		t.Fatalf("NewTag short EPC error = %v", err)
	}
	if _, err := NewTag([]byte{0x30, 0x01, 0x02}); !errors.Is(err, ErrInvalidEPCLength) {
		t.Fatalf("NewTag odd EPC error = %v", err)
	}
	epc := []byte{0x30, 0x01}
	tag, err := NewTag(epc)
	if err != nil {
		t.Fatal(err)
	}
	epc[0] = 0
	if got := tag.Hex(); got != "3001" {
		t.Fatalf("tag EPC = %s", got)
	}
}

func TestNewObservationValidatesMetadata(t *testing.T) {
	tag := mustTag(t, "3000")
	if _, err := NewObservation(tag, time.Time{}, "reader-a"); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("zero observation time error = %v", err)
	}
	seenAt := time.Date(2026, 8, 23, 0, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	observation, err := NewObservation(tag, seenAt, "reader-a")
	if err != nil {
		t.Fatal(err)
	}
	if observation.Tag().Hex() != "3000" || observation.SeenAt().Location() != time.UTC || observation.Source() != "reader-a" {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestStoreSnapshotIsSortedAndIndependent(t *testing.T) {
	store := NewStore()
	if err := store.Replace([]Tag{mustTag(t, "3002"), mustTag(t, "3000")}); err != nil {
		t.Fatal(err)
	}
	got := store.Snapshot()
	if values := []string{got[0].Hex(), got[1].Hex()}; !reflect.DeepEqual(values, []string{"3000", "3002"}) {
		t.Fatalf("snapshot = %v", values)
	}
	got[0] = mustTag(t, "3004")
	if store.Snapshot()[0].Hex() != "3000" {
		t.Fatal("snapshot mutation changed store")
	}
}

func TestLoadFileAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	limits := DefaultLimits()
	if err := os.WriteFile(path, []byte(`{"version":1,"tags":["3002","3000"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	if values := []string{got[0].Hex(), got[1].Hex()}; !reflect.DeepEqual(values, []string{"3002", "3000"}) {
		t.Fatalf("loaded = %v, want file order", values)
	}
	if _, err := LoadFile(path, Limits{MaxInputBytes: 8, MaxRecords: 2, MaxEPCBytes: MaxEPCBytes}); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("LoadFile limit error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"tags":["3000","3000"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path, limits); err == nil {
		t.Fatal("duplicate EPC accepted")
	}
}

func benchmarkTags(b *testing.B, count int) []Tag {
	b.Helper()
	tags := make([]Tag, count)
	for i := range tags {
		var err error
		tags[i], err = NewTag([]byte{0x30, 0x00, byte(i >> 8), byte(i)})
		if err != nil {
			b.Fatal(err)
		}
	}
	return tags
}

func BenchmarkStoreSnapshot100(b *testing.B) {
	store := NewStore()
	if err := store.Replace(benchmarkTags(b, 100)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = store.Snapshot()
	}
}

func BenchmarkLoadFile1000(b *testing.B) {
	path := filepath.Join(b.TempDir(), "inventory.json")
	hexes := make([]string, 0, 1000)
	for _, tag := range benchmarkTags(b, 1000) {
		hexes = append(hexes, `"`+tag.Hex()+`"`)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"tags":[`+strings.Join(hexes, ",")+`]}`), 0o600); err != nil {
		b.Fatal(err)
	}
	limits := DefaultLimits()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := LoadFile(path, limits); err != nil {
			b.Fatal(err)
		}
	}
}
