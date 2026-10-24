package llrp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"testing"
)

// referenceReadEvents is DecodeReadEvents as it was before the decoder
// stopped copying nested payloads: decode each list fully, then inspect it.
// The differential tests below hold the current decoder to its results and
// errors exactly.
func referenceReadEvents(body []byte, limits Limits) ([]*ReadEvent, error) {
	parameters, err := DecodeParameters(body, limits)
	if err != nil {
		return nil, err
	}
	events := make([]*ReadEvent, 0)
	for _, report := range parameters {
		if report.TV || report.Type != 240 {
			continue
		}
		inner, err := DecodeParameters(report.Data, limits)
		if err != nil {
			return nil, err
		}
		var id, pc []byte
		for _, p := range inner {
			switch p.Type {
			case 12:
				if !p.TV || len(p.Data) != 2 {
					return nil, fmt.Errorf("%w: invalid C1G2_PC", ErrMalformedTagReport)
				}
				pc = append([]byte(nil), p.Data...)
			case 13:
				if !p.TV || len(p.Data) != 12 {
					return nil, fmt.Errorf("%w: invalid 96-bit EPC", ErrMalformedTagReport)
				}
				id = append([]byte(nil), p.Data...)
			case 241:
				if p.TV || len(p.Data) < 2 {
					return nil, fmt.Errorf("%w: invalid variable EPC", ErrMalformedTagReport)
				}
				bits := int(binary.BigEndian.Uint16(p.Data[:2]))
				n := (bits + 7) / 8
				if bits == 0 || n > len(p.Data)-2 {
					return nil, fmt.Errorf("%w: EPC bit length %d", ErrMalformedTagReport, bits)
				}
				id = append([]byte(nil), p.Data[2:2+n]...)
			}
		}
		if len(id) == 0 {
			continue
		}
		events = append(events, &ReadEvent{ID: id, PC: pc})
	}
	return events, nil
}

func sameEvents(a, b []*ReadEvent) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i].ID, b[i].ID) || !bytes.Equal(a[i].PC, b[i].PC) || (a[i].PC == nil) != (b[i].PC == nil) {
			return false
		}
	}
	return true
}

func checkAgainstReference(t *testing.T, body []byte) {
	t.Helper()
	limits := DefaultLimits()
	want, wantErr := referenceReadEvents(body, limits)
	got, gotErr := DecodeReadEvents(body, limits)
	if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || !sameEvents(got, want) {
		t.Fatalf("body %x:\ngot  %v, %v\nwant %v, %v", body, got, gotErr, want, wantErr)
	}
}

func trd(params ...[]byte) []byte {
	inner := bytes.Join(params, nil)
	out := make([]byte, 4, 4+len(inner))
	binary.BigEndian.PutUint16(out[0:2], 240)
	binary.BigEndian.PutUint16(out[2:4], uint16(4+len(inner)))
	return append(out, inner...)
}

var (
	epc96  = append([]byte{0x80 | 13}, bytes.Repeat([]byte{0x30}, 12)...)
	pcTV   = []byte{0x80 | 12, 0x30, 0x00}
	epcTLV = []byte{0x00, 241, 0x00, 0x12, 0x00, 0x60, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
)

// TestDecodeReadEventsMatchesReference covers the orderings where the
// in-place decoder could differ: a malformed report before a structural
// error, missing EPCs, repeated EPCs, and events that must not alias body.
func TestDecodeReadEventsMatchesReference(t *testing.T) {
	for _, body := range [][]byte{
		nil,
		trd(epc96, pcTV),
		trd(epcTLV),
		bytes.Join([][]byte{trd(pcTV), trd(epc96), trd(epcTLV, epc96, pcTV)}, nil),
		bytes.Join([][]byte{trd([]byte{0x80 | 12, 0x30}), {0x00, 0x01}}, nil),             // bad PC, then truncated list
		bytes.Join([][]byte{trd([]byte{0x00, 241, 0x00, 0x06, 0x00, 0x00}), {0xff}}, nil), // zero-bit EPC, then unknown TV
		trd(epc96, []byte{0x00, 0x05, 0x00, 0x02}),                                        // inner length below header
	} {
		checkAgainstReference(t, body)
	}
	body := trd(epc96, pcTV)
	events, err := DecodeReadEvents(body, DefaultLimits())
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	for i := range body {
		body[i] = 0
	}
	if events[0].ID[0] != 0x30 || events[0].PC[0] != 0x30 {
		t.Fatal("events alias the report body")
	}
	events[0].ID = append(events[0].ID, 0xee)
	if events[0].PC[0] != 0x30 {
		t.Fatal("appending to ID overwrote PC")
	}
}

func FuzzDecodeReadEventsMatchesReference(f *testing.F) {
	f.Add(trd(epc96, pcTV))
	f.Add(bytes.Join([][]byte{trd(pcTV), trd(epcTLV)}, nil))
	f.Add(bytes.Join([][]byte{trd([]byte{0x80 | 12, 0x30}), {0x00, 0x01}}, nil))
	f.Fuzz(func(t *testing.T, body []byte) {
		checkAgainstReference(t, body)
	})
}
