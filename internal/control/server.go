package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MinTokenBytes is the shortest accepted bearer token.
const MinTokenBytes = 32

// Server limits. Every request and response path is bounded by these, by the
// per-route body limit passed to ReadJSON, and by the bounded state each
// application renders.
const (
	ReadHeaderTimeout = 2 * time.Second
	ReadTimeout       = 5 * time.Second
	WriteTimeout      = 5 * time.Second
	IdleTimeout       = 30 * time.Second
	MaxHeaderBytes    = 8 << 10
	DefaultMaxConns   = 16
)

// ServerConfig configures authentication and resource bounds.
type ServerConfig struct {
	// Token is required for TCP and optional for a Unix socket. It is
	// copied; the caller may clear its slice afterwards.
	Token []byte
	// MaxConns caps concurrent connections; excess connections are closed
	// immediately. Zero means DefaultMaxConns.
	MaxConns int
	// Logger receives audit records. Nil discards them.
	Logger *slog.Logger
}

// Server is a control HTTP server bound to one listener.
type Server struct {
	http      *http.Server
	listener  net.Listener
	transport Transport
	token     []byte
	logger    *slog.Logger
}

// New wraps a handler with request IDs, actor attribution, and, when a token
// is configured or the transport is TCP, fail-closed bearer authentication.
// It starts nothing; Serve does.
func New(listener net.Listener, transport Transport, handler http.Handler, c ServerConfig) (*Server, error) {
	if transport == TransportTCP && len(c.Token) == 0 {
		return nil, fmt.Errorf("%w: a TCP control listener requires a token", ErrConfig)
	}
	if len(c.Token) > 0 && len(c.Token) < MinTokenBytes {
		return nil, fmt.Errorf("%w: control token must be at least %d bytes", ErrConfig, MinTokenBytes)
	}
	if c.MaxConns == 0 {
		c.MaxConns = DefaultMaxConns
	}
	if c.MaxConns < 1 || c.MaxConns > 1024 {
		return nil, fmt.Errorf("%w: max control connections %d outside 1..1024", ErrConfig, c.MaxConns)
	}
	logger := c.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Server{listener: &limitListener{Listener: listener, max: int32(c.MaxConns)}, transport: transport, token: append([]byte(nil), c.Token...), logger: logger}
	s.http = &http.Server{
		Handler:           s.wrap(handler),
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
	return s, nil
}

// Serve serves until Shutdown or Close; it returns http.ErrServerClosed then.
func (s *Server) Serve() error { return s.http.Serve(s.listener) }

// Shutdown stops accepting and waits for active requests until ctx ends.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// Close closes the listener and all connections immediately.
func (s *Server) Close() error { return s.http.Close() }

// Addr returns the listener's address.
func (s *Server) Addr() net.Addr { return s.listener.Addr() }

type contextKey uint8

const (
	keyRequestID contextKey = iota
	keyActor
)

// RequestID returns the request's ID, also sent as the X-Request-ID response
// header.
func RequestID(r *http.Request) string {
	id, _ := r.Context().Value(keyRequestID).(string)
	return id
}

// Actor names who made a request: "unix" for the socket, or the TCP peer
// address. Forwarded headers are ignored; trusting them would let any client
// choose its recorded address.
func Actor(r *http.Request) string {
	actor, _ := r.Context().Value(keyActor).(string)
	return actor
}

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		actor := "unix"
		if s.transport == TransportTCP {
			actor = r.RemoteAddr
		}
		ctx := context.WithValue(context.WithValue(r.Context(), keyRequestID, id), keyActor, actor)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		if len(s.token) > 0 && !s.authorized(r) {
			Audit(s.logger, r, "auth", "unauthenticated")
			w.Header().Set("WWW-Authenticate", `Bearer realm="tagstrak"`)
			// Fixed body; nothing from the request is echoed, and the body
			// is never read.
			WriteError(w, http.StatusUnauthorized, "unauthenticated", "missing or invalid bearer token", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), s.token) == 1
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// limitListener closes connections beyond max instead of queueing them.
type limitListener struct {
	net.Listener
	max    int32
	active atomic.Int32
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.active.Add(1) > l.max {
			l.active.Add(-1)
			_ = c.Close()
			continue
		}
		return &limitConn{Conn: c, release: func() { l.active.Add(-1) }}, nil
	}
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }

// IsClosed reports whether err is Serve's normal termination.
func IsClosed(err error) bool { return errors.Is(err, http.ErrServerClosed) }
