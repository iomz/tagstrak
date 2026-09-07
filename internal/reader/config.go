// Package reader owns gosstrak-side LLRP sessions and their retry policy.
package reader

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/iomz/tagstrak/v2/llrp"
)

var (
	ErrConfig         = errors.New("reader: invalid configuration")
	ErrProtocol       = errors.New("reader: protocol violation")
	ErrBackpressure   = errors.New("reader: observation queue full")
	ErrRetryExhausted = errors.New("reader: reconnect budget exhausted")
	ErrAlreadyRun     = errors.New("reader: session already run")
)

// Config is copied at construction. MaxRetries counts reconnects across the
// entire run, including connections that completed a handshake.
type Config struct {
	Address          string
	Source           string
	ConnectTimeout   time.Duration
	HandshakeTimeout time.Duration
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	RetryMin         time.Duration
	RetryMax         time.Duration
	MaxRetries       int
	InitialMessageID uint32
	Limits           llrp.Limits
}

// DefaultConfig returns the default configuration for an LLRP reader session.
func DefaultConfig() Config {
	return Config{Address: "127.0.0.1:5084", Source: "reader-1", ConnectTimeout: 5 * time.Second,
		HandshakeTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 5 * time.Second,
		RetryMin: time.Second, RetryMax: 30 * time.Second, MaxRetries: 5, InitialMessageID: 1000,
		Limits: llrp.DefaultLimits()}
}

func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.Address)
	if err != nil {
		return fmt.Errorf("%w: address: %v", ErrConfig, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("%w: reader port must be 1-65535", ErrConfig)
	}
	if c.Source == "" || len(c.Source) > 256 {
		return fmt.Errorf("%w: source must contain 1-256 bytes", ErrConfig)
	}
	if c.ConnectTimeout <= 0 || c.HandshakeTimeout <= 0 || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.RetryMin <= 0 || c.RetryMax < c.RetryMin || c.MaxRetries < 0 {
		return fmt.Errorf("%w: positive timeouts, ordered backoff, and nonnegative retry budget required", ErrConfig)
	}
	if c.Limits.MaxFrameSize < llrp.MessageHeaderSize || c.Limits.MaxParameterSize < 4 || c.Limits.MaxParameters <= 0 {
		return fmt.Errorf("%w: explicit positive codec limits required", ErrConfig)
	}
	return nil
}

// backoff calculates the retry delay, starting at RetryMin and doubling for each
// subsequent retry until it reaches RetryMax.
func backoff(c Config, retry int) time.Duration {
	delay := c.RetryMin
	for i := 1; i < retry && delay < c.RetryMax; i++ {
		if delay > c.RetryMax/2 {
			return c.RetryMax
		}
		delay *= 2
	}
	return delay
}
