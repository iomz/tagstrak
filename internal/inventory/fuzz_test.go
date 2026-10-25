package inventory

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// fuzzLimits are small so that boundary-sized seeds stay small too.
var fuzzLimits = Limits{MaxInputBytes: 512, MaxRecords: 8, MaxEPCBytes: MaxEPCBytes}

func checkTags(t *testing.T, tags []Tag) {
	t.Helper()
	if len(tags) > fuzzLimits.MaxRecords {
		t.Fatalf("accepted %d records over the limit", len(tags))
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if n := len(tag.EPC()); n < MinEPCBytes || n > fuzzLimits.MaxEPCBytes {
			t.Fatalf("accepted %d-byte EPC", n)
		}
		if seen[tag.Hex()] {
			t.Fatalf("accepted duplicate tag %s", tag.Hex())
		}
		seen[tag.Hex()] = true
	}
}

func FuzzDecodeInventoryJSON(f *testing.F) {
	f.Add([]byte(`{"version":1,"tags":["3074257bf7194e4000001a85"]}`))
	f.Add([]byte(`{"version":1,"tags":["3074","3074"]}`))
	f.Add([]byte(`{"version":2,"tags":[]}`))
	f.Add([]byte(`{"version":1,"tags":["zz"]}`))
	f.Add([]byte(`{"version":1,"tags":[` + strings.Repeat(`"3074",`, 20) + `"3074"]}`))
	at := `{"version":1,"tags":["` + strings.Repeat("ab", int(fuzzLimits.MaxInputBytes-26)/2) + `"]}`
	f.Add([]byte(at))
	f.Add([]byte(at + " "))
	f.Fuzz(func(t *testing.T, data []byte) {
		decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(data), fuzzLimits.MaxInputBytes+1))
		if tags, err := decodeDocument(decoder, fuzzLimits); err == nil {
			checkTags(t, tags)
		}
	})
}
