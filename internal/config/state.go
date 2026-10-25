package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// State is the subscription state file tagstrak owns when subscriptions come
// from the management API. It is JSON so it can be inspected and backed up,
// and it shares the configuration file's subscription schema.
type State struct {
	Version       int            `json:"version"`
	Revision      uint64         `json:"revision"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// LoadState reads a state file. A missing file is an empty state at revision
// 0, so a new deployment starts with no subscriptions. Anything else that
// cannot be read or decoded strictly is an error, because silently starting
// empty would discard the operator's subscriptions.
func LoadState(path string) (State, error) {
	body, err := readBounded(path, MaxFileBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{Version: Version}, nil
		}
		return State{}, err
	}
	s, err := decodeState(body)
	if err != nil {
		return State{}, fmt.Errorf("%w: state %s: %v", ErrConfig, path, err)
	}
	return s, nil
}

// decodeState strictly decodes and validates a state body.
func decodeState(body []byte) (State, error) {
	var s State
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return State{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return State{}, errors.New("trailing data")
	}
	if s.Version != Version {
		return State{}, fmt.Errorf("version %d is not supported; want %d", s.Version, Version)
	}
	if err := validateSubscriptions(s.Subscriptions); err != nil {
		return State{}, err
	}
	return s, nil
}

// WriteState replaces the state file atomically: it writes a temporary file
// in the same directory with mode 0600, syncs it, renames it over the old
// file, and syncs the directory. A crash leaves either the old state or the
// new one, never a partial file.
func WriteState(path string, s State) error {
	s.Version = Version
	if s.Subscriptions == nil {
		s.Subscriptions = []Subscription{}
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encode state: %w", err)
	}
	body = append(body, '\n')
	if len(body) > MaxFileBytes {
		return fmt.Errorf("%w: state exceeds %d bytes", ErrConfig, MaxFileBytes)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("config: write state: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write state: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: write state: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("config: write state: %w", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync() // best effort; not supported on every platform
		_ = d.Close()
	}
	return nil
}
