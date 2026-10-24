package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
)

// MaxSecretBytes bounds a secret file.
const MaxSecretBytes = 4 << 10

// ErrSecret reports a secret that could not be resolved. Its messages name
// the source, never the value.
var ErrSecret = errors.New("config: secret unavailable")

const redacted = "[redacted]"

// Secret holds a resolved secret value. Every formatting path prints
// "[redacted]"; Reveal is the only way to read it.
type Secret struct {
	value []byte
}

// Reveal returns a copy of the secret value.
func (s Secret) Reveal() []byte { return bytes.Clone(s.value) }

// String implements fmt.Stringer.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer, so %#v is redacted too.
func (s Secret) GoString() string { return redacted }

// LogValue implements slog.LogValuer.
func (s Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON implements json.Marshaler.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText implements encoding.TextMarshaler, which some encoders prefer.
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// SecretSource names where a secret comes from: exactly one of an
// environment variable or a file. Prefix, such as "Bearer ", is prepended
// literally and is not secret.
//
// Value exists only to reject inline secrets with a clear error: the
// configuration file must stay safe to commit or share.
type SecretSource struct {
	Env    string  `toml:"env" json:"env"`
	File   string  `toml:"file" json:"file"`
	Prefix string  `toml:"prefix" json:"prefix"`
	Value  *string `toml:"value" json:"value"`
}

func (s SecretSource) validate() error {
	switch {
	case s.Value != nil:
		return errors.New("inline secret values are not allowed; use env or file")
	case s.Env != "" && s.File != "":
		return errors.New("set env or file, not both")
	case s.Env == "" && s.File == "":
		return errors.New("env or file is required")
	}
	return nil
}

// Resolve reads the secret. lookup is os.LookupEnv in production. A missing,
// empty, oversized or over-permissive source is an error, so a deployment
// with an unavailable secret fails at startup rather than sending
// unauthenticated requests.
func (s SecretSource) Resolve(lookup func(string) (string, bool)) (Secret, error) {
	if err := s.validate(); err != nil {
		return Secret{}, fmt.Errorf("%w: %v", ErrSecret, err)
	}
	var value []byte
	if s.Env != "" {
		v, ok := lookup(s.Env)
		if !ok || v == "" {
			return Secret{}, fmt.Errorf("%w: environment variable %s is unset or empty", ErrSecret, s.Env)
		}
		value = []byte(v)
	} else {
		v, err := readSecretFile(s.File)
		if err != nil {
			return Secret{}, err
		}
		value = v
	}
	return Secret{value: append([]byte(s.Prefix), value...)}, nil
}

// readSecretFile reads a private, regular, bounded secret file and trims one
// trailing newline, which editors and `echo` add.
func readSecretFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: secret file %s: %v", ErrSecret, path, errors.Unwrap(err))
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: secret file %s is not a regular file", ErrSecret, path)
	}
	// Unix permission bits are meaningless on Windows, where ACLs apply.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: secret file %s has mode %s; group and other permissions must be unset", ErrSecret, path, info.Mode().Perm())
	}
	if info.Size() > MaxSecretBytes {
		return nil, fmt.Errorf("%w: secret file %s exceeds %d bytes", ErrSecret, path, MaxSecretBytes)
	}
	body, err := readBounded(path, MaxSecretBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: secret file %s unreadable", ErrSecret, path)
	}
	body = bytes.TrimSuffix(body, []byte("\n"))
	body = bytes.TrimSuffix(body, []byte("\r"))
	if len(body) == 0 {
		return nil, fmt.Errorf("%w: secret file %s is empty", ErrSecret, path)
	}
	return body, nil
}
