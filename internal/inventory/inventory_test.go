package inventory

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
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

func TestFileRoundTripAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	limits := DefaultLimits()
	want := []Tag{mustTag(t, "3002"), mustTag(t, "3000")}
	if err := SaveFile(path, want, limits); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path, limits)
	if err != nil {
		t.Fatal(err)
	}
	if values := []string{got[0].Hex(), got[1].Hex()}; !reflect.DeepEqual(values, []string{"3000", "3002"}) {
		t.Fatalf("round trip = %v", values)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"tags":["3000","3000"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path, limits); err == nil {
		t.Fatal("duplicate EPC accepted")
	}
	if err := SaveFile(path, want, Limits{MaxInputBytes: 8, MaxRecords: 2, MaxEPCBytes: MaxEPCBytes}); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("SaveFile limit error = %v", err)
	}
}

func TestServiceConcurrentUpsertsPersistDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.json")
	service := NewService(NewStore(), path, DefaultLimits())
	values := []string{"3000", "3002", "3004", "3006"}
	var group sync.WaitGroup
	for _, value := range values {
		value := value
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := service.Upsert(mustTag(t, value)); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if got := service.Snapshot(); len(got) != len(values) {
		t.Fatalf("snapshot count = %d", len(got))
	}
	loaded, err := LoadFile(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(values) {
		t.Fatalf("persisted count = %d", len(loaded))
	}
}

func TestLoadCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.csv")
	if err := os.WriteFile(path, []byte("3000,0011000000000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tags, err := LoadCSV(path, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Hex() != "3000" {
		t.Fatalf("tags = %#v", tags)
	}
}

func TestLoadCSVRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.csv")
	if err := os.WriteFile(path, []byte("3000,0011000000000000\n3000,0011000000000001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxInputBytes = 24
	if _, err := LoadCSV(path, limits); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("error = %v", err)
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

func BenchmarkJSONRoundTrip1000(b *testing.B) {
	path := filepath.Join(b.TempDir(), "inventory.json")
	tags := benchmarkTags(b, 1000)
	limits := DefaultLimits()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := SaveFile(path, tags, limits); err != nil {
			b.Fatal(err)
		}
		if _, err := LoadFile(path, limits); err != nil {
			b.Fatal(err)
		}
	}
}
