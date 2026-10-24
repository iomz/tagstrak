package emulator

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzDecodeScenario(f *testing.F) {
	f.Add([]byte(`{"version":1,"seed":1,"repeat":true,"cycles":[["0001"]]}`))
	f.Add([]byte(`{"version":1,"seed":1,"cycles":[]}`))
	f.Add([]byte(`{"version":1,"seed":1,"cycles":[["0001","0001"]]}`))
	f.Add([]byte(`{"version":1,"seed":-9223372036854775808,"shuffle":true,"cycles":[["zz"]]}`))
	f.Add([]byte(`{"version":1} trailing`))
	f.Add([]byte(strings.Repeat("[", 4096)))
	head := `{"version":1,"seed":1,"cycles":[["0001"]],"x":"`
	f.Add([]byte(head + strings.Repeat("a", MaxScenarioBytes-len(head)-2) + `"}`))
	f.Add([]byte(head + strings.Repeat("a", MaxScenarioBytes-len(head)-1) + `"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := DecodeScenario(bytes.NewReader(data))
		if err != nil {
			return
		}
		if s.Cycles() == 0 {
			t.Fatal("accepted a scenario without cycles")
		}
		if len(data) > MaxScenarioBytes {
			t.Fatalf("accepted %d bytes over the limit", len(data))
		}
	})
}
