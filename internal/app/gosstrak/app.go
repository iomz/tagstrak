// Package gosstrak composes the reader, observation consumer, and telemetry.
package gosstrak

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/iomz/tagstrak/v2/internal/inventory"
	"github.com/iomz/tagstrak/v2/internal/reader"
)

var ErrDrainDeadline = errors.New("gosstrak: shutdown drain deadline exceeded")

// Consumer owns processing after reader ingestion. Consume must return when
// ctx is canceled, must not retain mutable shared state without synchronization,
// and must not start unowned goroutines. Errors are critical application failures.
type Consumer interface {
	Consume(context.Context, inventory.Observation) error
}

// Config is immutable after New. QueueCapacity counts observations, not frames.
type Config struct {
	Reader           reader.Config
	QueueCapacity    int
	ConsumeTimeout   time.Duration
	ShutdownTimeout  time.Duration
	TelemetryAddress string
}

func DefaultConfig() Config {
	return Config{Reader: reader.DefaultConfig(), QueueCapacity: 128, ConsumeTimeout: 5 * time.Second, ShutdownTimeout: 10 * time.Second, TelemetryAddress: "127.0.0.1:8080"}
}
func (c Config) Validate() error {
	if err := c.Reader.Validate(); err != nil {
		return err
	}
	if c.QueueCapacity < 1 || c.QueueCapacity > 100000 || c.ConsumeTimeout <= 0 || c.ShutdownTimeout <= 0 {
		return fmt.Errorf("%w: invalid queue or application timeout", reader.ErrConfig)
	}
	if _, _, err := net.SplitHostPort(c.TelemetryAddress); err != nil {
		return fmt.Errorf("%w: telemetry address: %v", reader.ErrConfig, err)
	}
	return nil
}

type App struct {
	config           Config
	session          *reader.Session
	consumer         Consumer
	queue            chan inventory.Observation
	started          atomic.Bool
	running          atomic.Bool
	draining         atomic.Bool
	processed        atomic.Uint64
	abandoned        atomic.Uint64
	consumerFailed   atomic.Bool
	telemetryAddress atomic.Value
}

func New(config Config, dialer reader.Dialer, consumer Consumer, logger *slog.Logger) (*App, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if consumer == nil {
		return nil, fmt.Errorf("%w: consumer is required", reader.ErrConfig)
	}
	session, err := reader.New(config.Reader, dialer, logger)
	if err != nil {
		return nil, err
	}
	return &App{config: config, session: session, consumer: consumer, queue: make(chan inventory.Observation, config.QueueCapacity)}, nil
}

// Run binds telemetry before dialing. It owns and joins the reader, consumer,
// HTTP server, and shutdown timer. Cancellation stops ingestion, then drains the
// accepted queue within ShutdownTimeout. Consumer failures cancel ingestion.
func (a *App) Run(ctx context.Context) error {
	if !a.started.CompareAndSwap(false, true) {
		return reader.ErrAlreadyRun
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", a.config.TelemetryAddress)
	if err != nil {
		return err
	}
	a.telemetryAddress.Store(listener.Addr().String())
	server := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(&limitedListener{Listener: listener}) }()
	a.running.Store(true)
	defer a.running.Store(false)
	readCtx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	consumeCtx, stopConsumer := context.WithCancel(context.WithoutCancel(ctx))
	defer stopConsumer()
	readerDone := make(chan error, 1)
	consumerDone := make(chan error, 1)
	go func() { err := a.session.Run(readCtx, a.queue); close(a.queue); readerDone <- err }()
	go func() { consumerDone <- a.consume(consumeCtx) }()
	var result error
	var readFinished, consumeFinished, serverFinished bool
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case result = <-readerDone:
		readFinished = true
	case result = <-consumerDone:
		consumeFinished = true
	case result = <-serverDone:
		serverFinished = true
	}
	a.draining.Store(true)
	stopReader()
	// A failed consumer cannot drain. For normal cancellation or reader failure,
	// preserve observations already accepted until the shared shutdown deadline.
	shutdownEnd := time.Now().Add(a.config.ShutdownTimeout)
	deadline := time.NewTimer(time.Until(shutdownEnd))
	defer deadline.Stop()
	timedOut := false
	for !readFinished || !consumeFinished {
		select {
		case err := <-readerDone:
			readFinished = true
			if err != nil && !errors.Is(err, context.Canceled) {
				result = errors.Join(result, err)
			}
		case err := <-consumerDone:
			consumeFinished = true
			if err != nil {
				result = errors.Join(result, err)
			}
		case <-deadline.C:
			timedOut = true
			stopConsumer()
			// Consumer and Dialer contracts require cancellation to unblock calls.
			if !readFinished {
				<-readerDone
				readFinished = true
			}
			if !consumeFinished {
				<-consumerDone
				consumeFinished = true
			}
		}
	}
	if timedOut {
		result = errors.Join(result, ErrDrainDeadline)
	}
	a.abandoned.Add(uint64(len(a.queue)))
	for range a.queue {
	} // Release accepted observations after an aborted drain.
	shutdownCtx, cancel := context.WithDeadline(context.Background(), shutdownEnd)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		result = errors.Join(result, err)
		_ = server.Close()
	}
	if !serverFinished {
		if err := <-serverDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (a *App) consume(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case observation, ok := <-a.queue:
			if !ok {
				return nil
			}
			callCtx, cancel := context.WithTimeout(ctx, a.config.ConsumeTimeout)
			err := a.consumer.Consume(callCtx, observation)
			if err == nil {
				err = callCtx.Err()
			}
			cancel()
			if err != nil {
				a.consumerFailed.Store(true)
				a.abandoned.Add(1)
				return fmt.Errorf("consume observation: %w", err)
			}
			a.processed.Add(1)
		}
	}
}
