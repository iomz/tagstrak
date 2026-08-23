package llrp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var (
	ErrMalformedTagReport = errors.New("llrp: malformed tag report")
	ErrMissingEPC         = errors.New("llrp: tag report has no EPC")
)

// DecodeReadEvents decodes TagReportData parameters from an RO_ACCESS_REPORT
// body. It validates every nested parameter before slicing its payload.
func DecodeReadEvents(body []byte, limits Limits) ([]*ReadEvent, error) {
	parameters, err := DecodeParameters(body, limits)
	if err != nil {
		return nil, err
	}
	events := make([]*ReadEvent, 0)
	for _, report := range parameters {
		if report.TV || report.Type != 240 {
			continue
		}
		event, err := decodeTagReportData(report.Data, limits)
		if errors.Is(err, ErrMissingEPC) {
			continue
		}
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func decodeTagReportData(data []byte, limits Limits) (*ReadEvent, error) {
	parameters, err := DecodeParameters(data, limits)
	if err != nil {
		return nil, err
	}
	var id, pc []byte
	for _, parameter := range parameters {
		switch parameter.Type {
		case 12: // C1G2_PC
			if !parameter.TV || len(parameter.Data) != 2 {
				return nil, fmt.Errorf("%w: invalid C1G2_PC", ErrMalformedTagReport)
			}
			pc = append([]byte(nil), parameter.Data...)
		case 13: // EPC_DATA TV form, fixed 96-bit EPC
			if !parameter.TV || len(parameter.Data) != 12 {
				return nil, fmt.Errorf("%w: invalid 96-bit EPC", ErrMalformedTagReport)
			}
			id = append([]byte(nil), parameter.Data...)
		case 241: // EPC_DATA TLV form, variable EPC length
			if parameter.TV || len(parameter.Data) < 2 {
				return nil, fmt.Errorf("%w: invalid variable EPC", ErrMalformedTagReport)
			}
			bits := int(binary.BigEndian.Uint16(parameter.Data[:2]))
			bytesNeeded := (bits + 7) / 8
			if bits == 0 || bytesNeeded > len(parameter.Data)-2 {
				return nil, fmt.Errorf("%w: EPC bit length %d", ErrMalformedTagReport, bits)
			}
			id = append([]byte(nil), parameter.Data[2:2+bytesNeeded]...)
		}
	}
	if len(id) == 0 {
		return nil, ErrMissingEPC
	}
	return &ReadEvent{ID: id, PC: pc}, nil
}
