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
//
// Nested payloads are inspected in place. The events share one backing array
// and their EPC and PC bytes one copy, so a report costs a few allocations
// rather than several per tag, and events never alias body.
func DecodeReadEvents(body []byte, limits Limits) ([]*ReadEvent, error) {
	// Validate the whole list first, so a structural error anywhere wins over
	// a malformed report earlier in the body, as when the list was decoded
	// before being inspected.
	reports := 0
	if err := walkParameters(body, limits, func(p Parameter) error {
		if !p.TV && p.Type == 240 {
			reports++
		}
		return nil
	}); err != nil {
		return nil, err
	}
	views := make([]tagView, 0, reports)
	size := 0
	err := walkParameters(body, limits, func(report Parameter) error {
		if report.TV || report.Type != 240 {
			return nil
		}
		view, err := decodeTagReportData(report.Data, limits)
		if errors.Is(err, ErrMissingEPC) {
			return nil
		}
		if err != nil {
			return err
		}
		views = append(views, view)
		size += len(view.id) + len(view.pc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	arena := make([]byte, 0, size)
	store := make([]ReadEvent, len(views))
	events := make([]*ReadEvent, len(views))
	for i, v := range views {
		// Full slice expressions keep an append to one field from
		// overwriting the next.
		start := len(arena)
		arena = append(arena, v.id...)
		store[i].ID = arena[start:len(arena):len(arena)]
		if v.pc != nil {
			start = len(arena)
			arena = append(arena, v.pc...)
			store[i].PC = arena[start:len(arena):len(arena)]
		}
		events[i] = &store[i]
	}
	return events, nil
}

// tagView is one tag report's EPC and PC as views into the report body.
type tagView struct{ id, pc []byte }

func decodeTagReportData(data []byte, limits Limits) (tagView, error) {
	if err := walkParameters(data, limits, skipParameter); err != nil {
		return tagView{}, err
	}
	var v tagView
	err := walkParameters(data, limits, func(parameter Parameter) error {
		switch parameter.Type {
		case 12: // C1G2_PC
			if !parameter.TV || len(parameter.Data) != 2 {
				return fmt.Errorf("%w: invalid C1G2_PC", ErrMalformedTagReport)
			}
			v.pc = parameter.Data
		case 13: // EPC_DATA TV form, fixed 96-bit EPC
			if !parameter.TV || len(parameter.Data) != 12 {
				return fmt.Errorf("%w: invalid 96-bit EPC", ErrMalformedTagReport)
			}
			v.id = parameter.Data
		case 241: // EPC_DATA TLV form, variable EPC length
			if parameter.TV || len(parameter.Data) < 2 {
				return fmt.Errorf("%w: invalid variable EPC", ErrMalformedTagReport)
			}
			bits := int(binary.BigEndian.Uint16(parameter.Data[:2]))
			bytesNeeded := (bits + 7) / 8
			if bits == 0 || bytesNeeded > len(parameter.Data)-2 {
				return fmt.Errorf("%w: EPC bit length %d", ErrMalformedTagReport, bits)
			}
			v.id = parameter.Data[2 : 2+bytesNeeded]
		}
		return nil
	})
	if err != nil {
		return tagView{}, err
	}
	if len(v.id) == 0 {
		return tagView{}, ErrMissingEPC
	}
	return v, nil
}

func skipParameter(Parameter) error { return nil }
