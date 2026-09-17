// Package emulator owns deterministic reader scenarios and LLRP serving.
package emulator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"

	"github.com/iomz/tagstrak/v2/internal/inventory"
)

const (
	MaxScenarioBytes = 1 << 20
	MaxCycles        = 1024
	MaxTags          = 100000
)

var ErrScenario = errors.New("emulator: invalid scenario")

// Scenario is immutable. Construction copies and sorts each inventory cycle.
type Scenario struct {
	cycles  [][]inventory.Tag
	seed    int64
	repeat  bool
	shuffle bool
}

// NewScenario validates all cycles before publishing a scenario. MaxTags bounds
// total records across cycles, including repeated tags in different cycles.
func NewScenario(cycles [][]inventory.Tag, seed int64, repeat, shuffle bool) (*Scenario, error) {
	if len(cycles) == 0 || len(cycles) > MaxCycles {
		return nil, fmt.Errorf("%w: expected 1-%d cycles", ErrScenario, MaxCycles)
	}
	s := &Scenario{seed: seed, repeat: repeat, shuffle: shuffle}
	total := 0
	for _, tags := range cycles {
		total += len(tags)
		if total > MaxTags {
			return nil, fmt.Errorf("%w: too many tags", ErrScenario)
		}
		store := inventory.NewStore()
		if err := store.Replace(tags); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrScenario, err)
		}
		s.cycles = append(s.cycles, store.Snapshot())
	}
	return s, nil
}

// DecodeScenario rejects unknown/repeated fields, trailing data, and oversized
// documents. EPC validation and duplicate identity handling belong to inventory.
func DecodeScenario(r io.Reader) (*Scenario, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxScenarioBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxScenarioBytes {
		return nil, fmt.Errorf("%w: document too large", ErrScenario)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("%w: expected object", ErrScenario)
	}
	var version int
	var seed int64
	var repeat, shuffle bool
	var cycles [][]string
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return nil, fmt.Errorf("%w: repeated field", ErrScenario)
		}
		seen[key] = true
		decode := func(target any) error {
			var raw json.RawMessage
			if err := d.Decode(&raw); err != nil {
				return err
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return ErrScenario
			}
			return json.Unmarshal(raw, target)
		}
		switch key {
		case "version":
			err = decode(&version)
		case "seed":
			err = decode(&seed)
		case "repeat":
			err = decode(&repeat)
		case "shuffle":
			err = decode(&shuffle)
		case "cycles":
			err = decode(&cycles)
		default:
			return nil, fmt.Errorf("%w: unknown field %q", ErrScenario, key)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrScenario, err)
		}
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data", ErrScenario)
	}
	if version != 1 || !seen["seed"] || !seen["cycles"] {
		return nil, fmt.Errorf("%w: version 1, seed, and cycles required", ErrScenario)
	}
	if len(cycles) == 0 || len(cycles) > MaxCycles {
		return nil, ErrScenario
	}
	parsed := make([][]inventory.Tag, len(cycles))
	total := 0
	for i, cycle := range cycles {
		if cycle == nil {
			return nil, fmt.Errorf("%w: cycle must be an array", ErrScenario)
		}
		total += len(cycle)
		if total > MaxTags {
			return nil, ErrScenario
		}
		for _, epc := range cycle {
			tag, err := inventory.ParseTag(epc)
			if err != nil {
				return nil, err
			}
			parsed[i] = append(parsed[i], tag)
		}
	}
	return NewScenario(parsed, seed, repeat, shuffle)
}

// Random is private to a cursor. Intn must return a value in [0,n).
type Random interface{ Intn(int) int }
type RandomFactory func(int64) Random

// SeededRandom returns an independent deterministic random source for seed.
func SeededRandom(seed int64) Random { return rand.New(rand.NewSource(seed)) }

// Catalog publishes whole scenarios atomically; a cycle holds one snapshot.
type Catalog struct {
	mu       sync.RWMutex
	scenario *Scenario
	revision uint64
}

func newCatalog(s *Scenario) *Catalog { return &Catalog{scenario: s, revision: 1} }
func (c *Catalog) snapshot() (*Scenario, uint64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scenario, c.revision
}
func (c *Catalog) replace(s *Scenario) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scenario = s
	c.revision++
}

// Cursor is owned by one connection. A new revision resets its cycle and RNG at
// the next Next call; message numbering remains owned by the connection.
type Cursor struct {
	catalog  *Catalog
	factory  RandomFactory
	revision uint64
	scenario *Scenario
	random   Random
	index    int
}

// Next returns a mutable copy of the next cycle and reports whether a cycle was
// available. It returns false without an error after a nonrepeating scenario is
// exhausted and adopts a replacement scenario at the next call.
func (c *Cursor) Next() ([]inventory.Tag, bool, error) {
	scenario, revision := c.catalog.snapshot()
	if c.revision != revision {
		c.scenario = scenario
		c.revision = revision
		c.index = 0
		c.random = c.factory(scenario.seed)
		if c.random == nil {
			return nil, false, ErrScenario
		}
	}
	if c.index == len(scenario.cycles) {
		if !scenario.repeat {
			return nil, false, nil
		}
		c.index = 0
	}
	tags := append([]inventory.Tag(nil), scenario.cycles[c.index]...)
	c.index++
	if scenario.shuffle {
		for i := len(tags) - 1; i > 0; i-- {
			j := c.random.Intn(i + 1)
			if j < 0 || j > i {
				return nil, false, fmt.Errorf("%w: random source out of range", ErrScenario)
			}
			tags[i], tags[j] = tags[j], tags[i]
		}
	}
	return tags, true, nil
}
