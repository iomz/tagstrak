package llrp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// MessageHeaderSize is the fixed size of every LLRP message header.
	MessageHeaderSize = 10

	defaultMaxFrameSize     = 1 << 20
	defaultMaxParameterSize = 1 << 15
	defaultMaxParameters    = 4096
)

var (
	ErrFrameTooSmall        = errors.New("llrp: frame too small")
	ErrFrameTooLarge        = errors.New("llrp: frame too large")
	ErrMalformedHeader      = errors.New("llrp: malformed message header")
	ErrLengthMismatch       = errors.New("llrp: message length mismatch")
	ErrParameterTruncated   = errors.New("llrp: truncated parameter")
	ErrParameterTooLarge    = errors.New("llrp: parameter too large")
	ErrParameterLimit       = errors.New("llrp: parameter count limit exceeded")
	ErrUnsupportedParameter = errors.New("llrp: unsupported TV parameter")
)

// Limits bounds work performed while decoding untrusted protocol data.
// Zero fields select package defaults.
type Limits struct {
	MaxFrameSize     uint32
	MaxParameterSize uint16
	MaxParameters    int
}

func (l Limits) normalized() Limits {
	if l.MaxFrameSize == 0 {
		l.MaxFrameSize = defaultMaxFrameSize
	}
	if l.MaxParameterSize == 0 {
		l.MaxParameterSize = defaultMaxParameterSize
	}
	if l.MaxParameters == 0 {
		l.MaxParameters = defaultMaxParameters
	}
	return l
}

// DefaultLimits returns conservative protocol decoding limits.
func DefaultLimits() Limits { return (Limits{}).normalized() }

// Header is the fixed LLRP message header.
// Type contains wire reserved/version/type bits, preserving protocol values
// such as KeepaliveHeader and ROAccessReportHeader.
type Header struct {
	Type   uint16
	Length uint32
	ID     uint32
}

// Message contains one complete LLRP frame. Payload is owned by Message.
type Message struct {
	Header  Header
	Payload []byte
}

// MarshalBinary encodes a complete message after validating its frame size.
func (m Message) MarshalBinary() ([]byte, error) {
	return EncodeMessage(m, DefaultLimits())
}

// EncodeMessage encodes one complete message within limits.
func EncodeMessage(m Message, limits Limits) ([]byte, error) {
	limits = limits.normalized()
	if uint64(MessageHeaderSize)+uint64(len(m.Payload)) > uint64(limits.MaxFrameSize) {
		return nil, ErrFrameTooLarge
	}
	length := uint64(MessageHeaderSize) + uint64(len(m.Payload))
	if length > uint64(^uint32(0)) {
		return nil, ErrFrameTooLarge
	}
	if m.Header.Length != 0 && m.Header.Length != uint32(length) {
		return nil, ErrLengthMismatch
	}
	frame := make([]byte, length)
	binary.BigEndian.PutUint16(frame[0:2], m.Header.Type)
	binary.BigEndian.PutUint32(frame[2:6], uint32(length))
	binary.BigEndian.PutUint32(frame[6:10], m.Header.ID)
	copy(frame[MessageHeaderSize:], m.Payload)
	return frame, nil
}

// DecodeMessage decodes exactly one complete frame. Extra bytes are rejected.
func DecodeMessage(frame []byte, limits Limits) (Message, error) {
	limits = limits.normalized()
	if len(frame) < MessageHeaderSize {
		return Message{}, ErrFrameTooSmall
	}
	if uint64(len(frame)) > uint64(limits.MaxFrameSize) {
		return Message{}, ErrFrameTooLarge
	}
	header := Header{
		Type:   binary.BigEndian.Uint16(frame[0:2]),
		Length: binary.BigEndian.Uint32(frame[2:6]),
		ID:     binary.BigEndian.Uint32(frame[6:10]),
	}
	if header.Length < MessageHeaderSize {
		return Message{}, fmt.Errorf("%w: length %d", ErrMalformedHeader, header.Length)
	}
	if header.Length > limits.MaxFrameSize {
		return Message{}, ErrFrameTooLarge
	}
	if uint64(header.Length) != uint64(len(frame)) {
		return Message{}, ErrLengthMismatch
	}
	payload := append([]byte(nil), frame[MessageHeaderSize:]...)
	return Message{Header: header, Payload: payload}, nil
}

// ReadMessage reads exactly one bounded frame without owning the reader.
func ReadMessage(r io.Reader, limits Limits) (Message, error) {
	limits = limits.normalized()
	headerBytes := make([]byte, MessageHeaderSize)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		return Message{}, err
	}
	length := binary.BigEndian.Uint32(headerBytes[2:6])
	if length < MessageHeaderSize {
		return Message{}, fmt.Errorf("%w: length %d", ErrMalformedHeader, length)
	}
	if length > limits.MaxFrameSize {
		return Message{}, ErrFrameTooLarge
	}
	frame := make([]byte, length)
	copy(frame, headerBytes)
	if _, err := io.ReadFull(r, frame[MessageHeaderSize:]); err != nil {
		return Message{}, err
	}
	return DecodeMessage(frame, limits)
}

// WriteMessage writes one complete frame and never closes w.
func WriteMessage(w io.Writer, m Message, limits Limits) error {
	frame, err := EncodeMessage(m, limits)
	if err != nil {
		return err
	}
	for len(frame) > 0 {
		n, writeErr := w.Write(frame)
		if n < 0 || n > len(frame) {
			return io.ErrShortWrite
		}
		frame = frame[n:]
		if writeErr != nil {
			return writeErr
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// Parameter is one decoded LLRP parameter. Data excludes its wire header.
type Parameter struct {
	Type uint16
	TV   bool
	Data []byte
}

// EncodeParameter encodes one parameter.
func EncodeParameter(p Parameter) ([]byte, error) {
	if p.TV {
		if p.Type > 0x7f {
			return nil, fmt.Errorf("%w: TV type %d", ErrMalformedHeader, p.Type)
		}
		total, ok := tvParameterSize(p.Type)
		if !ok {
			return nil, fmt.Errorf("%w: type %d", ErrUnsupportedParameter, p.Type)
		}
		if total != 1+len(p.Data) {
			return nil, fmt.Errorf("%w: TV type %d length %d", ErrLengthMismatch, p.Type, len(p.Data)+1)
		}
		return append([]byte{byte(0x80 | p.Type)}, p.Data...), nil
	}
	if p.Type > 0x03ff {
		return nil, fmt.Errorf("%w: TLV type %d", ErrMalformedHeader, p.Type)
	}
	length := 4 + len(p.Data)
	if length > int(^uint16(0)) {
		return nil, ErrParameterTooLarge
	}
	encoded := make([]byte, length)
	binary.BigEndian.PutUint16(encoded[0:2], p.Type)
	binary.BigEndian.PutUint16(encoded[2:4], uint16(length))
	copy(encoded[4:], p.Data)
	return encoded, nil
}

// EncodeParameters encodes a bounded sequence of parameters.
func EncodeParameters(parameters []Parameter, limits Limits) ([]byte, error) {
	limits = limits.normalized()
	if len(parameters) > limits.MaxParameters {
		return nil, ErrParameterLimit
	}
	encoded := make([]byte, 0)
	for _, parameter := range parameters {
		value, err := EncodeParameter(parameter)
		if err != nil {
			return nil, err
		}
		if len(value) > int(limits.MaxParameterSize) {
			return nil, ErrParameterTooLarge
		}
		encoded = append(encoded, value...)
	}
	return encoded, nil
}

// DecodeParameters parses bounded TLV and known fixed-width TV parameters.
// Unknown TLV parameters are preserved. Unknown TV parameters are rejected
// because their length is implicit and cannot be safely skipped.
func DecodeParameters(data []byte, limits Limits) ([]Parameter, error) {
	limits = limits.normalized()
	parameters := make([]Parameter, 0, minInt(len(data)/4, limits.MaxParameters))
	for len(data) > 0 {
		if len(parameters) >= limits.MaxParameters {
			return nil, ErrParameterLimit
		}
		if data[0]&0x80 != 0 {
			typeID := uint16(data[0] & 0x7f)
			total, ok := tvParameterSize(typeID)
			if !ok {
				return nil, fmt.Errorf("%w: type %d", ErrUnsupportedParameter, typeID)
			}
			if total > len(data) {
				return nil, ErrParameterTruncated
			}
			if total > int(limits.MaxParameterSize) {
				return nil, ErrParameterTooLarge
			}
			parameters = append(parameters, Parameter{Type: typeID, TV: true, Data: append([]byte(nil), data[1:total]...)})
			data = data[total:]
			continue
		}
		if len(data) < 4 {
			return nil, ErrParameterTruncated
		}
		typeID := binary.BigEndian.Uint16(data[0:2]) & 0x03ff
		length := int(binary.BigEndian.Uint16(data[2:4]))
		if length < 4 {
			return nil, fmt.Errorf("%w: parameter length %d", ErrParameterTruncated, length)
		}
		if length > int(limits.MaxParameterSize) {
			return nil, ErrParameterTooLarge
		}
		if length > len(data) {
			return nil, ErrParameterTruncated
		}
		parameters = append(parameters, Parameter{Type: typeID, Data: append([]byte(nil), data[4:length]...)})
		data = data[length:]
	}
	return parameters, nil
}

func tvParameterSize(typeID uint16) (int, bool) {
	total, ok := tvParameterSizes[typeID]
	return total, ok
}

var tvParameterSizes = map[uint16]int{
	4:  9,  // LAST_SEEN_TIMESTAMP_UTC
	6:  2,  // PEAK_RSSI
	7:  3,  // CHANNEL_INDEX
	8:  3,  // TAG_SEEN_COUNT
	12: 3,  // C1G2_PC
	13: 13, // EPC_DATA with 96-bit EPC
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
