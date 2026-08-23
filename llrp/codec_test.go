package llrp

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestEncodeDecodeMessage(t *testing.T) {
	want := Message{Header: Header{Type: KeepaliveHeader, ID: 42}, Payload: []byte{1, 2, 3}}
	frame, err := EncodeMessage(want, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeMessage(frame, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Type != want.Header.Type || got.Header.ID != want.Header.ID || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	frame[10] = 99
	if got.Payload[0] == 99 {
		t.Fatal("decoded payload aliases input")
	}
}

func TestDecodeMessageRejectsMalformedLengths(t *testing.T) {
	cases := [][]byte{
		make([]byte, MessageHeaderSize-1),
		{0, 1, 0, 0, 0, 9, 0, 0, 0, 0},
		{0, 1, 0, 0, 0, 11, 0, 0, 0, 0},
	}
	for _, input := range cases {
		if _, err := DecodeMessage(input, DefaultLimits()); err == nil {
			t.Fatalf("DecodeMessage(%v) succeeded", input)
		}
	}
}

func TestDecodeMessageEnforcesFrameLimit(t *testing.T) {
	frame := make([]byte, MessageHeaderSize)
	frame[5] = MessageHeaderSize
	if _, err := DecodeMessage(frame, Limits{MaxFrameSize: 9}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("error = %v, want ErrFrameTooLarge", err)
	}
}

func TestReadWriteMessage(t *testing.T) {
	want := Message{Header: Header{Type: KeepaliveAckHeader, ID: 7}, Payload: []byte{9}}
	var buf bytes.Buffer
	if err := WriteMessage(&buf, want, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	got, err := ReadMessage(&buf, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if got.Header != (Header{Type: KeepaliveAckHeader, Length: 11, ID: 7}) || !bytes.Equal(got.Payload, []byte{9}) {
		t.Fatalf("got %#v", got)
	}
	if _, err := ReadMessage(bytes.NewReader(nil), DefaultLimits()); !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v, want io.EOF", err)
	}
}

func TestDecodeParametersPreservesTLVAndKnownTV(t *testing.T) {
	input := append([]byte{0, 240, 0, 6, 1, 2}, C1G2PC(0x3000)...)
	got, err := DecodeParameters(input, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Type != 240 || got[0].TV || !bytes.Equal(got[0].Data, []byte{1, 2}) {
		t.Fatalf("TLV = %#v", got)
	}
	if got[1].Type != 12 || !got[1].TV || !bytes.Equal(got[1].Data, []byte{0x30, 0}) {
		t.Fatalf("TV = %#v", got[1])
	}
}

func TestEncodeParametersRoundTrip(t *testing.T) {
	want := []Parameter{
		{Type: 240, Data: []byte{1, 2}},
		{Type: 12, TV: true, Data: []byte{0x30, 0}},
	}
	encoded, err := EncodeParameters(want, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeParameters(encoded, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || got[0].Type != want[0].Type || got[1].Type != want[1].Type || !bytes.Equal(got[0].Data, want[0].Data) || !bytes.Equal(got[1].Data, want[1].Data) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestEncodeParametersRejectsUnknownTV(t *testing.T) {
	if _, err := EncodeParameters([]Parameter{{Type: 1, TV: true, Data: []byte{0}}}, DefaultLimits()); !errors.Is(err, ErrUnsupportedParameter) {
		t.Fatalf("error = %v, want ErrUnsupportedParameter", err)
	}
}

func TestDecodeParametersRejectsUnsafeTVAndTruncation(t *testing.T) {
	if _, err := DecodeParameters([]byte{0x80}, DefaultLimits()); !errors.Is(err, ErrUnsupportedParameter) {
		t.Fatalf("unknown TV error = %v", err)
	}
	if _, err := DecodeParameters([]byte{0x8c, 0x30}, DefaultLimits()); !errors.Is(err, ErrParameterTruncated) {
		t.Fatalf("truncated TV error = %v", err)
	}
	if _, err := DecodeParameters([]byte{0, 240, 0, 3}, DefaultLimits()); err == nil {
		t.Fatal("short TLV succeeded")
	}
}

func FuzzDecodeMessageNeverPanics(f *testing.F) {
	f.Add([]byte{4, 62, 0, 0, 0, 10, 0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeMessage(data, DefaultLimits())
	})
}

func FuzzDecodeParametersNeverPanics(f *testing.F) {
	f.Add([]byte{0, 240, 0, 4})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeParameters(data, DefaultLimits())
	})
}

func BenchmarkDecodeMessage(b *testing.B) {
	frame, err := EncodeMessage(Message{Header: Header{Type: ROAccessReportHeader}, Payload: make([]byte, 1024)}, DefaultLimits())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeMessage(frame, DefaultLimits()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecodeParameters(b *testing.B) {
	data, err := EncodeParameters([]Parameter{
		{Type: 240, Data: bytes.Repeat([]byte{0xaa}, 256)},
		{Type: 12, TV: true, Data: []byte{0x30, 0}},
	}, DefaultLimits())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeParameters(data, DefaultLimits()); err != nil {
			b.Fatal(err)
		}
	}
}
