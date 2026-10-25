package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const fileVersion = 1

var (
	ErrInputTooLarge = errors.New("inventory: input exceeds limit")
	ErrTooManyTags   = errors.New("inventory: record count exceeds limit")
	ErrUnsupported   = errors.New("inventory: unsupported document version")
)

// Limits bounds inventory persistence resources.
type Limits struct {
	MaxInputBytes int64
	MaxRecords    int
	MaxEPCBytes   int
}

// DefaultLimits returns limits for local inventory snapshots.
func DefaultLimits() Limits {
	return Limits{MaxInputBytes: 16 << 20, MaxRecords: 100_000, MaxEPCBytes: MaxEPCBytes}
}

func (l Limits) validate() error {
	if l.MaxInputBytes <= 0 || l.MaxRecords <= 0 || l.MaxEPCBytes < MinEPCBytes || l.MaxEPCBytes > MaxEPCBytes {
		return errors.New("inventory: invalid limits")
	}
	return nil
}

// LoadFile parses a versioned inventory document within limits.
func LoadFile(path string, limits Limits) ([]Tag, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > limits.MaxInputBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrInputTooLarge, info.Size())
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(io.LimitReader(file, limits.MaxInputBytes+1))
	return decodeDocument(decoder, limits)
}

func decodeDocument(decoder *json.Decoder, limits Limits) ([]Tag, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("inventory: document must be an object")
	}

	var version int
	var tags []Tag
	seenVersion := false
	seenTags := false
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch name {
		case "version":
			if seenVersion || decoder.Decode(&version) != nil {
				return nil, errors.New("inventory: invalid version")
			}
			seenVersion = true
		case "tags":
			if seenTags {
				return nil, errors.New("inventory: duplicate tags field")
			}
			decoded, err := decodeTags(decoder, limits)
			if err != nil {
				return nil, err
			}
			tags = decoded
			seenTags = true
		default:
			return nil, fmt.Errorf("inventory: unknown field %q", name)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if decoder.More() {
		return nil, errors.New("inventory: trailing JSON values")
	}
	if !seenVersion || !seenTags {
		return nil, errors.New("inventory: document requires version and tags")
	}
	if version != fileVersion {
		return nil, fmt.Errorf("%w: %d", ErrUnsupported, version)
	}
	return tags, nil
}

func decodeTags(decoder *json.Decoder, limits Limits) ([]Tag, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return nil, errors.New("inventory: tags must be an array")
	}
	tags := make([]Tag, 0)
	seen := make(map[string]struct{})
	for decoder.More() {
		if len(tags) == limits.MaxRecords {
			return nil, ErrTooManyTags
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		tag, err := ParseTag(value)
		if err != nil {
			return nil, err
		}
		if len(tag.EPC()) > limits.MaxEPCBytes {
			return nil, fmt.Errorf("%w: %d bytes", ErrInputTooLarge, len(tag.EPC()))
		}
		if _, exists := seen[tag.key()]; exists {
			return nil, fmt.Errorf("inventory: duplicate EPC %s", tag.Hex())
		}
		seen[tag.key()] = struct{}{}
		tags = append(tags, tag)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return tags, nil
}
