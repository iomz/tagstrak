// Package config decodes the versioned gosstrak configuration file and
// resolves secrets from their sources.
//
// The file is TOML (.toml) or JSON (.json) with one schema. Decoding is
// strict: unknown keys, trailing data, an unsupported version, or a file over
// MaxFileBytes is an error, so a typo cannot be silently ignored. YAML is not
// accepted, because YAML 1.1 parsers resolve unquoted scalars implicitly and
// would turn a GS1 company prefix with a leading zero into a number.
//
// Scalar settings are pointers: nil means "not set in the file", so the
// caller's default stands, and an explicit zero stays distinguishable from an
// omitted field. The package checks the file's shape and secret sources; the
// application maps the settings onto its own types and validates their
// meaning, so this schema stays an external contract independent of internal
// configuration types.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Version is the only supported file version.
const Version = 1

// MaxFileBytes bounds the configuration file.
const MaxFileBytes = 1 << 20

// ErrConfig reports an invalid configuration file.
var ErrConfig = errors.New("config: invalid configuration")

// File is the decoded configuration file.
type File struct {
	Version       int            `toml:"version" json:"version"`
	Reader        Reader         `toml:"reader" json:"reader"`
	Telemetry     Telemetry      `toml:"telemetry" json:"telemetry"`
	Runtime       Runtime        `toml:"runtime" json:"runtime"`
	Report        Report         `toml:"report" json:"report"`
	Destinations  []Destination  `toml:"destinations" json:"destinations"`
	Subscriptions []Subscription `toml:"subscriptions" json:"subscriptions"`
}

// Reader configures the LLRP reader session.
type Reader struct {
	Address           *string   `toml:"address" json:"address"`
	Source            *string   `toml:"source" json:"source"`
	ConnectTimeout    *Duration `toml:"connect_timeout" json:"connect_timeout"`
	HandshakeTimeout  *Duration `toml:"handshake_timeout" json:"handshake_timeout"`
	ReadTimeout       *Duration `toml:"read_timeout" json:"read_timeout"`
	WriteTimeout      *Duration `toml:"write_timeout" json:"write_timeout"`
	RetryMin          *Duration `toml:"retry_min" json:"retry_min"`
	RetryMax          *Duration `toml:"retry_max" json:"retry_max"`
	MaxRetries        *int      `toml:"max_retries" json:"max_retries"`
	MaxFrameBytes     *uint32   `toml:"max_frame_bytes" json:"max_frame_bytes"`
	MaxParameterBytes *uint16   `toml:"max_parameter_bytes" json:"max_parameter_bytes"`
	MaxParameters     *int      `toml:"max_parameters" json:"max_parameters"`
}

// Telemetry configures the read-only telemetry listener.
type Telemetry struct {
	Address *string `toml:"address" json:"address"`
}

// Runtime configures the observation pipeline.
type Runtime struct {
	QueueCapacity   *int      `toml:"queue_capacity" json:"queue_capacity"`
	ConsumeTimeout  *Duration `toml:"consume_timeout" json:"consume_timeout"`
	ShutdownTimeout *Duration `toml:"shutdown_timeout" json:"shutdown_timeout"`
}

// Report configures delivery bounds.
type Report struct {
	QueueCapacity  *int      `toml:"queue_capacity" json:"queue_capacity"`
	BatchEvents    *int      `toml:"batch_events" json:"batch_events"`
	BatchBytes     *int      `toml:"batch_bytes" json:"batch_bytes"`
	FlushInterval  *Duration `toml:"flush_interval" json:"flush_interval"`
	RequestTimeout *Duration `toml:"request_timeout" json:"request_timeout"`
	MaxAttempts    *int      `toml:"max_attempts" json:"max_attempts"`
	BackoffBase    *Duration `toml:"backoff_base" json:"backoff_base"`
	BackoffMax     *Duration `toml:"backoff_max" json:"backoff_max"`
	RetryBudget    *Duration `toml:"retry_budget" json:"retry_budget"`
}

// Destination is a named HTTP report destination. Header values are secrets.
type Destination struct {
	Name    string                  `toml:"name" json:"name"`
	URL     string                  `toml:"url" json:"url"`
	Headers map[string]SecretSource `toml:"headers" json:"headers"`
}

// Subscription is a named set of patterns and the destinations its matches
// are routed to.
type Subscription struct {
	ID           string   `toml:"id" json:"id"`
	Patterns     []string `toml:"patterns" json:"patterns"`
	Destinations []string `toml:"destinations" json:"destinations"`
}

// Duration is a Go duration string such as "5s".
type Duration time.Duration

// UnmarshalText parses a Go duration string. Both decoders use it.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("duration %q: %w", text, err)
	}
	*d = Duration(v)
	return nil
}

// Load reads and strictly decodes a configuration file. The format is chosen
// by extension.
func Load(path string) (File, error) {
	body, err := readBounded(path, MaxFileBytes)
	if err != nil {
		return File{}, err
	}
	var f File
	switch ext := strings.ToLower(filepath.Ext(path)); ext {
	case ".toml":
		md, err := toml.Decode(string(body), &f)
		if err != nil {
			return File{}, fmt.Errorf("%w: %s: %v", ErrConfig, path, err)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, len(undecoded))
			for i, k := range undecoded {
				keys[i] = k.String()
			}
			sort.Strings(keys)
			return File{}, fmt.Errorf("%w: %s: unknown keys: %s", ErrConfig, path, strings.Join(keys, ", "))
		}
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return File{}, fmt.Errorf("%w: %s: %v", ErrConfig, path, err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return File{}, fmt.Errorf("%w: %s: trailing data after the configuration object", ErrConfig, path)
		}
	default:
		return File{}, fmt.Errorf("%w: %s: unsupported format %q; use .toml or .json", ErrConfig, path, ext)
	}
	if err := f.validate(); err != nil {
		return File{}, fmt.Errorf("%w: %s: %v", ErrConfig, path, err)
	}
	return f, nil
}

// validate checks the file's shape. Meaning, such as whether a pattern
// compiles or a destination name is unique, is the application's to check.
func (f File) validate() error {
	if f.Version != Version {
		return fmt.Errorf("version %d is not supported; want %d", f.Version, Version)
	}
	for i, d := range f.Destinations {
		if d.Name == "" {
			return fmt.Errorf("destinations[%d].name is required", i)
		}
		if d.URL == "" {
			return fmt.Errorf("destinations[%d].url is required", i)
		}
		for name, source := range d.Headers {
			if err := source.validate(); err != nil {
				return fmt.Errorf("destinations[%d].headers.%s: %v", i, name, err)
			}
		}
	}
	for i, s := range f.Subscriptions {
		if s.ID == "" {
			return fmt.Errorf("subscriptions[%d].id is required", i)
		}
		if len(s.Patterns) == 0 {
			return fmt.Errorf("subscriptions[%d].patterns is required", i)
		}
	}
	return nil
}

// readBounded reads a whole file, failing rather than truncating when it
// exceeds limit.
func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfig, err)
	}
	defer file.Close()
	body, err := readAllBounded(file, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrConfig, path, err)
	}
	return body, nil
}

// readAllBounded reads r to the end, failing rather than truncating when it
// holds more than limit bytes.
func readAllBounded(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("exceeds %d bytes", limit)
	}
	return body, nil
}
