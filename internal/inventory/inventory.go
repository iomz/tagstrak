// Package inventory owns protocol-neutral RFID tag identity and inventory state.
package inventory

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sync"
)

const (
	// MinEPCBytes is one EPC memory word.
	MinEPCBytes = 2
	// MaxEPCBytes is the largest EPC length representable by an LLRP PC field.
	MaxEPCBytes = 62
)

var (
	ErrInvalidEPCLength = errors.New("inventory: invalid EPC length")
	ErrInvalidEPC       = errors.New("inventory: invalid EPC encoding")
)

// Tag is immutable RFID tag identity. Its EPC bytes are never exposed directly.
type Tag struct {
	epc string
}

// NewTag validates and copies EPC bytes.
func NewTag(epc []byte) (Tag, error) {
	if len(epc) < MinEPCBytes || len(epc) > MaxEPCBytes || len(epc)%2 != 0 {
		return Tag{}, fmt.Errorf("%w: got %d bytes, want even length from %d through %d", ErrInvalidEPCLength, len(epc), MinEPCBytes, MaxEPCBytes)
	}
	return Tag{epc: string(epc)}, nil
}

// ParseTag decodes a hexadecimal EPC.
func ParseTag(value string) (Tag, error) {
	epc, err := hex.DecodeString(value)
	if err != nil {
		return Tag{}, fmt.Errorf("%w: %v", ErrInvalidEPC, err)
	}
	return NewTag(epc)
}

// EPC returns a defensive copy of EPC bytes.
func (t Tag) EPC() []byte {
	return []byte(t.epc)
}

// Hex returns canonical lowercase hexadecimal EPC.
func (t Tag) Hex() string {
	return hex.EncodeToString([]byte(t.epc))
}

func (t Tag) key() string {
	return t.epc
}

// Store provides concurrent, deterministic inventory snapshots.
type Store struct {
	mu   sync.RWMutex
	tags map[string]Tag
}

// NewStore constructs an empty inventory.
func NewStore() *Store {
	return &Store{tags: make(map[string]Tag)}
}

// Snapshot returns tags sorted by raw EPC bytes.
func (s *Store) Snapshot() []Tag {
	s.mu.RLock()
	tags := make([]Tag, 0, len(s.tags))
	for _, tag := range s.tags {
		tags = append(tags, tag)
	}
	s.mu.RUnlock()
	sortTags(tags)
	return tags
}

// Replace atomically replaces inventory after validating duplicate identities.
func (s *Store) Replace(tags []Tag) error {
	next, err := tagMap(tags)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.tags = next
	s.mu.Unlock()
	return nil
}

func tagMap(tags []Tag) (map[string]Tag, error) {
	next := make(map[string]Tag, len(tags))
	for _, tag := range tags {
		validated, err := NewTag(tag.EPC())
		if err != nil {
			return nil, err
		}
		if _, exists := next[validated.key()]; exists {
			return nil, fmt.Errorf("%w: duplicate EPC %s", ErrInvalidEPC, validated.Hex())
		}
		next[validated.key()] = validated
	}
	return next, nil
}

func sortTags(tags []Tag) {
	slices.SortFunc(tags, func(a, b Tag) int {
		return bytes.Compare([]byte(a.epc), []byte(b.epc))
	})
}
