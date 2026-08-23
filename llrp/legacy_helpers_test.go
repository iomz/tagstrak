package llrp

import (
	"bytes"
	"testing"
)

func TestPack(t *testing.T) {
	got := Pack([]interface{}{uint16(349), uint16(11), uint8(0)})
	want := []byte{1, 93, 0, 11, 0}
	if !bytes.Equal(got, want) {
		t.Fatalf("Pack = %v, want %v", got, want)
	}
}
