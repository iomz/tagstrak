package emulator

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/iomz/tagstrak/v2/llrp"
)

// requestedKeepalive returns the interval requested by SET_READER_CONFIG, or
// the configured default when KeepaliveSpec is omitted. It rejects resets,
// unsupported parameters, and intervals outside the configured time budget.
func requestedKeepalive(m llrp.Message, c Config) (time.Duration, error) {
	if m.Header.Type != llrp.SetReaderConfigHeader || len(m.Payload) < 1 || m.Payload[0] != 0 {
		return 0, fmt.Errorf("%w: expected SET_READER_CONFIG without reset", ErrProtocol)
	}
	parameters, err := llrp.DecodeParameters(m.Payload[1:], c.limits())
	if err != nil {
		return 0, err
	}
	interval := c.KeepaliveInterval
	seen := false
	for _, p := range parameters {
		if p.TV || p.Type != 220 || seen || len(p.Data) != 5 || p.Data[0] != 1 {
			return 0, fmt.Errorf("%w: unsupported keepalive configuration", ErrProtocol)
		}
		seen = true
		interval = time.Duration(binary.BigEndian.Uint32(p.Data[1:])) * time.Millisecond
	}
	if interval < 10*time.Millisecond || interval > time.Hour || interval+c.AckTimeout >= c.ReadTimeout {
		return 0, fmt.Errorf("%w: keepalive interval outside configured budget", ErrProtocol)
	}
	return interval, nil
}

type incoming struct {
	message  llrp.Message
	err      error
	received time.Time
}

// serve has one writer (this goroutine), one bounded inbound queue, one read
// worker, and one cancellation watcher. All are joined before connection return.
func (s *Server) serve(parent context.Context, conn net.Conn) error {
	ctx, cancel := context.WithCancel(parent)
	watcher := make(chan struct{})
	go func() { defer close(watcher); <-ctx.Done(); _ = conn.Close() }()
	var readDone chan struct{}
	defer func() {
		cancel()
		_ = conn.Close()
		if readDone != nil {
			<-readDone
		}
		<-watcher
	}()
	write := func(m llrp.Message, end time.Time) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		deadline := time.Now().Add(s.config.WriteTimeout)
		if !end.IsZero() && end.Before(deadline) {
			deadline = end
		}
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		return llrp.WriteMessage(conn, m, s.config.limits())
	}
	handshakeEnd := time.Now().Add(s.config.HandshakeTimeout)
	if err := write(llrp.ReaderEventNotificationMessage(1, uint64(s.clock.Now().UnixMicro())), handshakeEnd); err != nil {
		return err
	}
	if err := conn.SetReadDeadline(handshakeEnd); err != nil {
		return err
	}
	request, err := llrp.ReadMessage(conn, s.config.limits())
	if err != nil {
		return err
	}
	interval, err := requestedKeepalive(request, s.config)
	if err != nil {
		return err
	}
	if err = write(llrp.SetReaderConfigResponseMessage(request.Header.ID), handshakeEnd); err != nil {
		return err
	}
	input := make(chan incoming, 1)
	readDone = make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			err := conn.SetReadDeadline(time.Now().Add(s.config.ReadTimeout))
			var message llrp.Message
			if err == nil {
				message, err = llrp.ReadMessage(conn, s.config.limits())
			}
			select {
			case input <- incoming{message: message, err: err, received: s.clock.Now()}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	cursor := &Cursor{catalog: s.catalog, factory: s.random}
	reportID := uint32(1000)
	keepaliveID := uint32(0x80000000)
	report := func() error {
		tags, ok, err := cursor.Next()
		if err != nil || !ok {
			return err
		}
		end := time.Now().Add(s.config.WriteTimeout)
		err = writeCycle(tags, s.config.FrameBytes, func(body []byte) error {
			if err := write(llrp.ROAccessReportMessage(body, reportID), end); err != nil {
				return err
			}
			reportID++
			s.update(func(v *Status) { v.Reports++ })
			return nil
		})
		if err == nil {
			s.update(func(v *Status) { v.Cycles++ })
		}
		return err
	}
	// Start each connection at cycle zero, independent of other clients or time.
	if err = report(); err != nil {
		return err
	}
	reportTimer := s.clock.NewTimer(s.config.ReportInterval)
	keepaliveTimer := s.clock.NewTimer(interval)
	var ackTimer Timer
	var ackC <-chan time.Time
	var pending uint32
	var ackDeadline time.Time
	defer func() {
		reportTimer.Stop()
		keepaliveTimer.Stop()
		if ackTimer != nil {
			ackTimer.Stop()
		}
	}()
	handle := func(in incoming) error {
		if in.err != nil {
			return in.err
		}
		m := in.message
		if m.Header.Type != llrp.KeepaliveAckHeader || len(m.Payload) != 0 || ackC == nil || m.Header.ID != pending {
			return fmt.Errorf("%w: unexpected message or keepalive ACK ID", ErrProtocol)
		}
		if !in.received.Before(ackDeadline) {
			return ErrKeepaliveTimeout
		}
		ackTimer.Stop()
		ackTimer = nil
		ackC = nil
		keepaliveTimer = s.clock.NewTimer(interval)
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Prioritize an already-read ACK before a simultaneously due timeout/report.
		select {
		case in := <-input:
			if err := handle(in); err != nil {
				return err
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case in := <-input:
			if err := handle(in); err != nil {
				return err
			}
		case <-reportTimer.C():
			if err := report(); err != nil {
				return err
			}
			reportTimer = s.clock.NewTimer(s.config.ReportInterval)
		case <-keepaliveTimer.C():
			pending = keepaliveID
			keepaliveID++
			if err := write(llrp.KeepaliveMessage(pending), time.Time{}); err != nil {
				return err
			}
			s.update(func(v *Status) { v.Keepalives++ })
			ackDeadline = s.clock.Now().Add(s.config.AckTimeout)
			ackTimer = s.clock.NewTimer(s.config.AckTimeout)
			ackC = ackTimer.C()
		case <-ackC:
			// ACK may have arrived while both select cases became ready.
			select {
			case in := <-input:
				if err := handle(in); err != nil {
					return err
				}
			default:
				return ErrKeepaliveTimeout
			}
		}
	}
}
