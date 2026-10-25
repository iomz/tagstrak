// Package rig composes emulator serving and opt-in local control.
package rig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/iomz/tagstrak/v2/internal/control"
	"github.com/iomz/tagstrak/v2/internal/emulator"
)

// Config keeps local control separate from the LLRP data-plane address.
type Config struct {
	Emulator        emulator.Config
	ControlSocket   string
	ShutdownTimeout time.Duration
}

// DefaultConfig returns the default emulator application configuration with
// local control disabled.
func DefaultConfig() Config {
	return Config{Emulator: emulator.DefaultConfig(), ShutdownTimeout: 5 * time.Second}
}

// Validate reports an error wrapping emulator.ErrConfig when the emulator
// settings, shutdown timeout, or optional control socket path are invalid.
func (c Config) Validate() error {
	if err := c.Emulator.Validate(); err != nil {
		return err
	}
	if c.ShutdownTimeout < 10*time.Millisecond || c.ShutdownTimeout > time.Minute {
		return fmt.Errorf("%w: shutdown timeout must be 10ms-1m", emulator.ErrConfig)
	}
	if c.ControlSocket != "" {
		if !filepath.IsAbs(c.ControlSocket) {
			return fmt.Errorf("%w: control socket must be absolute", emulator.ErrConfig)
		}
	}
	return nil
}

type App struct {
	config  Config
	server  *emulator.Server
	logger  *slog.Logger
	started atomic.Bool
}

// New validates its inputs and constructs an idle application without opening
// listeners or starting workers.
func New(config Config, scenario *emulator.Scenario, clock emulator.Clock, random emulator.RandomFactory, logger *slog.Logger) (*App, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	server, err := emulator.New(config.Emulator, scenario, clock, random, logger)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &App{config: config, server: server, logger: logger}, nil
}

// Status returns a race-safe snapshot of the emulator's operational state.
func (a *App) Status() emulator.Status { return a.server.Status() }

// Run owns both transports. No existing socket is removed; a private parent
// directory prevents access before chmod and supplies the local access boundary.
func (a *App) Run(ctx context.Context) error {
	if !a.started.CompareAndSwap(false, true) {
		return emulator.ErrAlreadyRun
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.config.ControlSocket == "" {
		return a.server.Run(ctx)
	}
	listener, transport, err := control.Listen(control.ListenConfig{Socket: a.config.ControlSocket})
	if err != nil {
		if errors.Is(err, control.ErrConfig) {
			return fmt.Errorf("%w: %w", emulator.ErrConfig, err)
		}
		return err
	}
	// Four connections preserve tagstrak-rig's original local-control budget.
	controlServer, err := control.New(listener, transport, a.control(), control.ServerConfig{MaxConns: 4, Logger: a.logger})
	if err != nil {
		_ = listener.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	controlDone := make(chan error, 1)
	dataDone := make(chan error, 1)
	go func() { controlDone <- controlServer.Serve() }()
	go func() { dataDone <- a.server.Run(ctx) }()
	var result error
	var dataFinished, controlFinished bool
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case result = <-dataDone:
		dataFinished = true
	case result = <-controlDone:
		controlFinished = true
	}
	cancel()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), a.config.ShutdownTimeout)
	defer cancelShutdown()
	if err := controlServer.Shutdown(shutdown); err != nil {
		result = errors.Join(result, err)
		_ = controlServer.Close()
	}
	if !dataFinished {
		if err := <-dataDone; err != nil && !errors.Is(err, context.Canceled) {
			result = errors.Join(result, err)
		}
	}
	if !controlFinished {
		if err := <-controlDone; err != nil && !control.IsClosed(err) {
			result = errors.Join(result, err)
		}
	}
	return result
}
