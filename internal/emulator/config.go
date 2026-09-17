package emulator

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/iomz/tagstrak/v2/llrp"
)

var (
	ErrConfig           = errors.New("emulator: invalid configuration")
	ErrProtocol         = errors.New("emulator: protocol violation")
	ErrAlreadyRun       = errors.New("emulator: server already run")
	ErrKeepaliveTimeout = errors.New("emulator: keepalive ACK timeout")
)

// Config bounds connection resources and wall-clock network operations.
type Config struct {
	Address           string
	MaxClients        int
	ReportInterval    time.Duration
	KeepaliveInterval time.Duration
	AckTimeout        time.Duration
	HandshakeTimeout  time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	FrameBytes        uint32
}

// DefaultConfig returns the default configuration for an emulator server.
func DefaultConfig() Config {
	return Config{Address: "127.0.0.1:5084", MaxClients: 8, ReportInterval: time.Second, KeepaliveInterval: 10 * time.Second, AckTimeout: 5 * time.Second, HandshakeTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Second, FrameBytes: 1500}
}

// Validate reports an error wrapping ErrConfig when an address, resource
// limit, duration, or frame budget is outside the supported range.
func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.Address)
	if err != nil {
		return fmt.Errorf("%w: address: %v", ErrConfig, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return fmt.Errorf("%w: port must be 0-65535", ErrConfig)
	}
	if c.MaxClients < 1 || c.MaxClients > 128 {
		return fmt.Errorf("%w: max clients must be 1-128", ErrConfig)
	}
	for _, d := range []time.Duration{c.ReportInterval, c.KeepaliveInterval, c.AckTimeout, c.HandshakeTimeout, c.ReadTimeout, c.WriteTimeout} {
		if d < 10*time.Millisecond || d > time.Hour {
			return fmt.Errorf("%w: durations must be 10ms-1h", ErrConfig)
		}
	}
	if c.KeepaliveInterval+c.AckTimeout >= c.ReadTimeout {
		return fmt.Errorf("%w: read timeout must exceed keepalive interval plus ACK timeout", ErrConfig)
	}
	if c.FrameBytes < 128 || c.FrameBytes > 1<<20 {
		return fmt.Errorf("%w: frame bytes must be 128-1048576", ErrConfig)
	}
	return nil
}
func (c Config) limits() llrp.Limits {
	l := llrp.DefaultLimits()
	l.MaxFrameSize = c.FrameBytes
	return l
}

// Clock controls scenario pacing. Network deadlines always use real time so a
// paused test clock cannot leave a socket blocked. Methods must be concurrent-safe.
type Clock interface {
	Now() time.Time
	NewTimer(time.Duration) Timer
}
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}
type SystemClock struct{}

func (SystemClock) Now() time.Time                 { return time.Now() }
func (SystemClock) NewTimer(d time.Duration) Timer { return systemTimer{time.NewTimer(d)} }

type systemTimer struct{ *time.Timer }

func (t systemTimer) C() <-chan time.Time { return t.Timer.C }
