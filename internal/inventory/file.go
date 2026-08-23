package inventory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	defer file.Close()

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

// SaveFile atomically replaces path with a validated, versioned snapshot.
func SaveFile(path string, tags []Tag, limits Limits) error {
	if err := limits.validate(); err != nil {
		return err
	}
	if len(tags) > limits.MaxRecords {
		return ErrTooManyTags
	}
	if _, err := tagMap(tags); err != nil {
		return err
	}
	tags = append([]Tag(nil), tags...)
	sortTags(tags)
	for _, tag := range tags {
		if len(tag.EPC()) > limits.MaxEPCBytes {
			return fmt.Errorf("%w: %d bytes", ErrInputTooLarge, len(tag.EPC()))
		}
	}

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".inventory-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}

	writer := bufio.NewWriter(&limitedWriter{writer: temp, remaining: limits.MaxInputBytes})
	if err := writeDocument(writer, tags); err != nil {
		temp.Close()
		return err
	}
	if err := writer.Flush(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, ErrInputTooLarge
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}

func writeDocument(writer io.Writer, tags []Tag) error {
	values := make([]string, len(tags))
	for i, tag := range tags {
		values[i] = tag.Hex()
	}
	encoder := json.NewEncoder(writer)
	return encoder.Encode(struct {
		Version int      `json:"version"`
		Tags    []string `json:"tags"`
	}{Version: fileVersion, Tags: values})
}
