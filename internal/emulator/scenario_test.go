package emulator

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

func tags(t *testing.T, values ...string) []inventory.Tag {
	t.Helper()
	var result []inventory.Tag
	for _, v := range values {
		tag, err := inventory.ParseTag(v)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, tag)
	}
	return result
}
func scenario(t *testing.T) *Scenario {
	t.Helper()
	s, err := NewScenario([][]inventory.Tag{tags(t, "303400000000000000000001", "303400000000000000000002", "303400000000000000000003"), {}, tags(t, "0001")}, 42, true, true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestScenarioReproducibleAndImmutable(t *testing.T) {
	original := tags(t, "0001", "0002", "0003", "0004", "0005", "0006")
	s, err := NewScenario([][]inventory.Tag{original}, 42, true, true)
	if err != nil {
		t.Fatal(err)
	}
	original[0] = inventory.Tag{}
	collect := func(seed int64) [][]string {
		copy := *s
		copy.seed = seed
		c := Cursor{catalog: newCatalog(&copy), factory: SeededRandom}
		var result [][]string
		for i := 0; i < 10; i++ {
			cycle, ok, err := c.Next()
			if err != nil || !ok {
				t.Fatal(err)
			}
			var values []string
			for _, tag := range cycle {
				values = append(values, tag.Hex())
			}
			result = append(result, values)
			cycle[0] = inventory.Tag{}
		}
		return result
	}
	a, b := collect(42), collect(42)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed changed sequence")
	}
	if reflect.DeepEqual(a, collect(43)) {
		t.Fatal("different seed did not change sequence")
	}
}
func TestScenarioDecodeRejectsMalformed(t *testing.T) {
	for _, input := range []string{
		`{}`, `{"version":1,"seed":null,"cycles":[[]]}`, `{"version":1,"seed":1,"cycles":[null]}`, `{"version":2,"seed":1,"cycles":[[]]}`, `{"version":1,"seed":1,"cycles":[]}`,
		`{"version":1,"seed":1,"seed":2,"cycles":[[]]}`,
		`{"version":1,"seed":1,"cycles":[["00"]]}`,
		`{"version":1,"seed":1,"cycles":[["0001","0001"]]}`,
		`{"version":1,"seed":1,"cycles":[[]],"other":1}`,
		`{"version":1,"seed":1,"cycles":[[]]} {}`,
		strings.Repeat(" ", MaxScenarioBytes+1),
	} {
		if _, err := DecodeScenario(strings.NewReader(input)); err == nil {
			t.Errorf("accepted %.100s", input)
		}
	}
	s, err := DecodeScenario(strings.NewReader(`{"version":1,"seed":0,"repeat":false,"cycles":[["0002","0001"],[]]}`))
	if err != nil {
		t.Fatal(err)
	}
	c := Cursor{catalog: newCatalog(s), factory: SeededRandom}
	cycle, ok, err := c.Next()
	if err != nil || !ok || cycle[0].Hex() != "0001" {
		t.Fatal(cycle, ok, err)
	}
	cycle, ok, err = c.Next()
	if err != nil || !ok || len(cycle) != 0 {
		t.Fatal(cycle, ok, err)
	}
	if _, ok, err = c.Next(); ok || err != nil {
		t.Fatal("finite scenario did not end", err)
	}
}
func TestUpdateAtCycleBoundary(t *testing.T) {
	initial := scenario(t)
	catalog := newCatalog(initial)
	c := Cursor{catalog: catalog, factory: SeededRandom}
	cycle, _, _ := c.Next()
	replacement, err := NewScenario([][]inventory.Tag{tags(t, "0009")}, 1, true, false)
	if err != nil {
		t.Fatal(err)
	}
	catalog.replace(replacement)
	if len(cycle) != 3 {
		t.Fatal("in-flight snapshot changed")
	}
	next, _, err := c.Next()
	if err != nil || len(next) != 1 || next[0].Hex() != "0009" {
		t.Fatal(next, err)
	}
}
func TestReportPackingUsesExactFrameBudget(t *testing.T) {
	input := tags(t, "303400000000000000000001", "303400000000000000000002")
	p, err := llrp.NewTagReportDataParam(input[0].EPC(), 0x3000)
	if err != nil {
		t.Fatal(err)
	}
	max := uint32(llrp.MessageHeaderSize + 2*len(p))
	for _, tc := range []struct {
		budget uint32
		frames int
	}{{max, 1}, {max - 1, 2}} {
		var frames [][]byte
		err := writeCycle(input, tc.budget, func(b []byte) error { frames = append(frames, bytes.Clone(b)); return nil })
		if err != nil {
			t.Fatal(err)
		}
		if len(frames) != tc.frames {
			t.Fatalf("frames=%d", len(frames))
		}
		var count int
		for _, frame := range frames {
			if uint32(len(frame)+10) > tc.budget {
				t.Fatal("oversized frame")
			}
			events, err := llrp.DecodeReadEvents(frame, llrp.DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			count += len(events)
		}
		if count != 2 {
			t.Fatal(count)
		}
	}
}
