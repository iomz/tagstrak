package reader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/llrp"
)

type State string

const (
	Idle         State = "idle"
	Connecting   State = "connecting"
	Handshaking  State = "handshaking"
	Serving      State = "serving"
	Keepalive    State = "keepalive"
	Reporting    State = "reporting"
	Reconnecting State = "reconnecting"
	Stopping     State = "stopping"
	Stopped      State = "stopped"
	Failed       State = "failed"
)

// Snapshot is a race-safe copy. Counters are cumulative across reconnects.
type Snapshot struct {
	State          State         `json:"state"`
	Ready          bool          `json:"ready"`
	Attempts       uint64        `json:"attempts"`
	Reconnects     uint64        `json:"reconnects"`
	Frames         uint64        `json:"frames"`
	Reports        uint64        `json:"reports"`
	Observations   uint64        `json:"observations"`
	Keepalives     uint64        `json:"keepalives"`
	QueueOverflows uint64        `json:"queue_overflows"`
	Failures       uint64        `json:"failures"`
	LastError      string        `json:"last_error,omitempty"`
	RetryDelay     time.Duration `json:"retry_delay_ns"`
}

// Dialer must honor cancellation and return a connection owned by the caller.
type Dialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type Session struct {
	config  Config
	dialer  Dialer
	logger  *slog.Logger
	started atomic.Bool
	mu      sync.RWMutex
	status  Snapshot
}

// New validates without I/O or goroutines. A Session can be run exactly once.
func New(config Config, dialer Dialer, logger *slog.Logger) (*Session, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if dialer == nil {
		return nil, fmt.Errorf("%w: dialer is required", ErrConfig)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Session{config: config, dialer: dialer, logger: logger, status: Snapshot{State: Idle}}, nil
}
func (s *Session) Snapshot() Snapshot        { s.mu.RLock(); defer s.mu.RUnlock(); return s.status }
func (s *Session) update(fn func(*Snapshot)) { s.mu.Lock(); defer s.mu.Unlock(); fn(&s.status) }
func (s *Session) transition(state State) {
	previous := s.Snapshot().State
	s.update(func(v *Snapshot) {
		v.State = state
		v.Ready = state == Serving || state == Keepalive || state == Reporting
	})
	if state == Keepalive || state == Reporting || (state == Serving && previous != Handshaking) {
		s.logger.Debug("reader state", "source", s.config.Source, "state", state)
	} else {
		s.logger.Info("reader state", "source", s.config.Source, "state", state)
	}
}

// Run owns all connections and its cancellation watcher. The caller owns out
// and closes it only after Run returns. Overflow fails the run without retry;
// observations already handed off remain valid. No delivery is silently dropped.
func (s *Session) Run(ctx context.Context, out chan<- inventory.Observation) (result error) {
	if !s.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	defer func() {
		s.transition(Stopping)
		if result != nil && ctx.Err() == nil {
			s.transition(Failed)
		} else {
			s.transition(Stopped)
		}
	}()
	if out == nil {
		return fmt.Errorf("%w: output queue is required", ErrConfig)
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.transition(Connecting)
		s.update(func(v *Snapshot) { v.Attempts++; v.RetryDelay = 0 })
		dialCtx, cancel := context.WithTimeout(ctx, s.config.ConnectTimeout)
		conn, err := s.dialer.DialContext(dialCtx, "tcp", s.config.Address)
		cancel()
		if err == nil {
			err = s.connected(ctx, conn, out)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.update(func(v *Snapshot) { v.Failures++; v.LastError = err.Error() })
		s.logger.Warn("reader connection failed", "source", s.config.Source, "error", err)
		if !retryable(err) {
			return err
		}
		if attempt >= s.config.MaxRetries {
			return fmt.Errorf("%w: %w", ErrRetryExhausted, err)
		}
		delay := backoff(s.config, attempt+1)
		s.update(func(v *Snapshot) { v.Reconnects++; v.RetryDelay = delay })
		s.transition(Reconnecting)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func retryable(err error) bool {
	if errors.Is(err, ErrProtocol) || errors.Is(err, ErrBackpressure) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Session) connected(ctx context.Context, conn net.Conn, out chan<- inventory.Observation) error {
	// Closing the connection interrupts both reads and writes immediately. Join
	// the watcher before returning so no goroutine outlives this connection.
	done := make(chan struct{})
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer func() { close(done); _ = conn.Close(); <-joined }()
	s.transition(Handshaking)
	handshakeEnd := time.Now().Add(s.config.HandshakeTimeout)
	read := func(deadline time.Time) (llrp.Message, error) {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return llrp.Message{}, err
		}
		m, err := llrp.ReadMessage(conn, s.config.Limits)
		if err == nil {
			s.update(func(v *Snapshot) { v.Frames++ })
		}
		return m, err
	}
	write := func(m llrp.Message, end time.Time) error {
		deadline := time.Now().Add(s.config.WriteTimeout)
		if !end.IsZero() && end.Before(deadline) {
			deadline = end
		}
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		return llrp.WriteMessage(conn, m, s.config.Limits)
	}
	notification, err := read(handshakeEnd)
	if err != nil {
		return err
	}
	if err = validateNotification(notification, s.config.Limits, true); err != nil {
		return err
	}
	request := llrp.SetReaderConfigMessage(s.config.InitialMessageID)
	if err = write(request, handshakeEnd); err != nil {
		return err
	}
	for {
		response, err := read(handshakeEnd)
		if err != nil {
			return err
		}
		if response.Header.Type == llrp.KeepaliveHeader {
			if len(response.Payload) != 0 {
				return fmt.Errorf("%w: keepalive payload", ErrProtocol)
			}
			if err = write(llrp.KeepaliveAckMessage(response.Header.ID), handshakeEnd); err != nil {
				return err
			}
			s.update(func(v *Snapshot) { v.Keepalives++ })
			continue
		}
		if err = validateConfigResponse(response, request.Header.ID, s.config.Limits); err != nil {
			return err
		}
		break
	}
	s.update(func(v *Snapshot) { v.LastError = "" })
	s.transition(Serving)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		message, err := read(time.Now().Add(s.config.ReadTimeout))
		if err != nil {
			return err
		}
		switch message.Header.Type {
		case llrp.KeepaliveHeader:
			if len(message.Payload) != 0 {
				return fmt.Errorf("%w: keepalive payload", ErrProtocol)
			}
			s.transition(Keepalive)
			if err = write(llrp.KeepaliveAckMessage(message.Header.ID), time.Time{}); err != nil {
				return err
			}
			s.update(func(v *Snapshot) { v.Keepalives++ })
		case llrp.ROAccessReportHeader:
			s.transition(Reporting)
			observations, err := decodeObservations(message.Payload, s.config.Limits, s.config.Source, time.Now())
			if err != nil {
				return fmt.Errorf("%w: %w", ErrProtocol, err)
			}
			s.update(func(v *Snapshot) { v.Reports++ })
			for _, observation := range observations {
				if err := ctx.Err(); err != nil {
					return err
				}
				select {
				case out <- observation:
					s.update(func(v *Snapshot) { v.Observations++ })
				default:
					s.update(func(v *Snapshot) { v.QueueOverflows++ })
					return ErrBackpressure
				}
			}
		case llrp.ReaderEventNotificationHeader:
			if err = validateNotification(message, s.config.Limits, false); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unexpected message type %#x", ErrProtocol, message.Header.Type)
		}
		s.transition(Serving)
	}
}
