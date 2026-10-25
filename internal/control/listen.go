// Package control serves management APIs over a hardened HTTP transport.
//
// It owns transport concerns only: listener rules, server limits,
// authentication, request IDs, strict JSON request bodies, structured errors,
// and audit records. Each application supplies its own routes, so no
// application type is imported here (#2: consumer-defined service interfaces).
//
// # Listeners
//
// Control is off unless configured. A Unix socket must have an absolute path
// in a private directory; it is created with mode 0600, never replaces an
// existing path, and trusts the same local user. A TCP listener must be on a
// loopback address and always requires a bearer token, because loopback is
// reachable by every local user and process and by browsers through DNS
// rebinding. Non-loopback addresses are refused: TLS terminates outside the
// process, in an SSH tunnel or a reverse proxy that reaches loopback with the
// token. Forwarded headers are never trusted.
package control

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
)

// ErrConfig reports an invalid control configuration.
var ErrConfig = errors.New("control: invalid configuration")

// maxSocketPath is the longest Unix socket path this platform can bind:
// sockaddr_un's path array less its terminating NUL, 103 bytes on macOS and
// 107 on Linux.
var maxSocketPath = len(syscall.RawSockaddrUnix{}.Path) - 1

// Transport says how a listener authenticates its peers.
type Transport uint8

const (
	// TransportUnix trusts the same local user through filesystem
	// permissions.
	TransportUnix Transport = iota + 1
	// TransportTCP is loopback-only and requires a bearer token.
	TransportTCP
)

// ListenConfig selects at most one listener. Both empty disables control.
type ListenConfig struct {
	// Socket is an absolute Unix socket path in a private directory.
	Socket string
	// Address is a loopback host:port. "localhost" means 127.0.0.1.
	Address string
}

// Enabled reports whether a listener is configured.
func (c ListenConfig) Enabled() bool { return c.Socket != "" || c.Address != "" }

// Validate checks the configuration without touching the filesystem or
// network.
func (c ListenConfig) Validate() error {
	switch {
	case c.Socket != "" && c.Address != "":
		return fmt.Errorf("%w: set a control socket or a control address, not both", ErrConfig)
	case c.Socket != "" && !filepath.IsAbs(c.Socket):
		return fmt.Errorf("%w: control socket path must be absolute", ErrConfig)
	case len(c.Socket) > maxSocketPath:
		return fmt.Errorf("%w: control socket path is %d bytes; this platform allows at most %d", ErrConfig, len(c.Socket), maxSocketPath)
	case c.Address != "":
		_, err := loopback(c.Address)
		return err
	}
	return nil
}

// loopback resolves a control address, refusing anything that is not a
// loopback IP literal or "localhost". Hostnames other than localhost are
// refused so that DNS cannot widen the binding.
func loopback(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("%w: control address: %v", ErrConfig, err)
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return "", fmt.Errorf("%w: control address must be loopback (127.0.0.0/8 or ::1); remote control is not supported, use a tunnel or proxy", ErrConfig)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// Listen opens the configured listener. A Unix socket's parent directory must
// exist, be a directory, and have no group or other permissions, which keeps
// the socket unreachable before its own mode is set. Closing the listener
// removes a socket it created.
func Listen(c ListenConfig) (net.Listener, Transport, error) {
	if err := c.Validate(); err != nil {
		return nil, 0, err
	}
	if c.Socket != "" {
		directory, err := os.Stat(filepath.Dir(c.Socket))
		if err != nil {
			return nil, 0, err
		}
		if !directory.IsDir() || directory.Mode().Perm()&0o077 != 0 {
			return nil, 0, fmt.Errorf("%w: control socket parent must be a private directory (0700)", ErrConfig)
		}
		// net.Listen fails on an existing path, so nothing is replaced.
		l, err := net.Listen("unix", c.Socket)
		if err != nil {
			return nil, 0, err
		}
		if err := os.Chmod(c.Socket, 0o600); err != nil {
			_ = l.Close()
			return nil, 0, err
		}
		return l, TransportUnix, nil
	}
	if c.Address == "" {
		return nil, 0, fmt.Errorf("%w: control is not configured", ErrConfig)
	}
	address, err := loopback(c.Address)
	if err != nil {
		return nil, 0, err
	}
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, 0, err
	}
	return l, TransportTCP, nil
}
