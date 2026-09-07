package reader

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

func validateNotification(m llrp.Message, limits llrp.Limits, initial bool) error {
	if m.Header.Type != llrp.ReaderEventNotificationHeader {
		return fmt.Errorf("%w: expected reader notification", ErrProtocol)
	}
	params, err := llrp.DecodeParameters(m.Payload, limits)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	containers, attempts := 0, 0
	for _, p := range params {
		if p.TV || p.Type != 246 {
			return fmt.Errorf("%w: invalid notification data", ErrProtocol)
		}
		containers++
		nested, err := llrp.DecodeParameters(p.Data, limits)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrProtocol, err)
		}
		for _, n := range nested {
			if n.Type == 256 {
				attempts++
				if n.TV || len(n.Data) != 2 || binary.BigEndian.Uint16(n.Data) != 0 {
					return fmt.Errorf("%w: connection attempt rejected", ErrProtocol)
				}
			}
		}
	}
	if containers != 1 || attempts > 1 || (initial && attempts != 1) {
		return fmt.Errorf("%w: missing or repeated notification data", ErrProtocol)
	}
	return nil
}

func validateConfigResponse(m llrp.Message, id uint32, limits llrp.Limits) error {
	if m.Header.Type != llrp.SetReaderConfigResponseHeader || m.Header.ID != id {
		return fmt.Errorf("%w: config response type or message ID mismatch", ErrProtocol)
	}
	params, err := llrp.DecodeParameters(m.Payload, limits)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	statuses := 0
	for _, p := range params {
		if p.Type != 287 {
			continue
		}
		statuses++
		if p.TV || len(p.Data) < 4 {
			return fmt.Errorf("%w: malformed LLRP status", ErrProtocol)
		}
		code := binary.BigEndian.Uint16(p.Data[:2])
		descriptionLength := int(binary.BigEndian.Uint16(p.Data[2:4]))
		if descriptionLength > len(p.Data)-4 {
			return fmt.Errorf("%w: truncated status description", ErrProtocol)
		}
		if _, err := llrp.DecodeParameters(p.Data[4+descriptionLength:], limits); err != nil {
			return fmt.Errorf("%w: %w", ErrProtocol, err)
		}
		if code != 0 {
			return fmt.Errorf("%w: reader rejected configuration, status %d", ErrProtocol, code)
		}
	}
	if statuses != 1 {
		return fmt.Errorf("%w: expected one LLRP status", ErrProtocol)
	}
	return nil
}

// Validate the entire batch before handing any observation to the application.
// The current domain supports whole EPC words; reject other bit lengths rather
// than silently padding them into a different identifier.
func decodeObservations(body []byte, limits llrp.Limits, source string, now time.Time) ([]inventory.Observation, error) {
	params, err := llrp.DecodeParameters(body, limits)
	if err != nil {
		return nil, err
	}
	for _, p := range params {
		if p.TV || p.Type != 240 {
			continue
		}
		nested, err := llrp.DecodeParameters(p.Data, limits)
		if err != nil {
			return nil, err
		}
		epcs := 0
		for _, n := range nested {
			if n.Type == 13 || n.Type == 241 {
				epcs++
			}
			if n.Type == 241 {
				if n.TV || len(n.Data) < 2 {
					return nil, llrp.ErrMalformedTagReport
				}
				bits := int(binary.BigEndian.Uint16(n.Data[:2]))
				if bits == 0 || bits%16 != 0 || bits/8 != len(n.Data)-2 {
					return nil, llrp.ErrMalformedTagReport
				}
			}
		}
		if epcs != 1 {
			return nil, llrp.ErrMalformedTagReport
		}
	}
	events, err := llrp.DecodeReadEvents(body, limits)
	if err != nil {
		return nil, err
	}
	observations := make([]inventory.Observation, 0, len(events))
	for _, event := range events {
		tag, err := inventory.NewTag(event.ID)
		if err != nil {
			return nil, err
		}
		observation, err := inventory.NewObservation(tag, now, source)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, nil
}
