// Package golemu composes emulator serving and opt-in local control.
package golemu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

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
	return &App{config: config, server: server}, nil
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
	directory, err := os.Stat(filepath.Dir(a.config.ControlSocket))
	if err != nil {
		return err
	}
	if !directory.IsDir() || directory.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: control parent must be a private directory (0700)", emulator.ErrConfig)
	}
	listener, err := net.Listen("unix", a.config.ControlSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(a.config.ControlSocket, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	httpServer := &http.Server{Handler: a.control(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	controlDone := make(chan error, 1)
	dataDone := make(chan error, 1)
	go func() { controlDone <- httpServer.Serve(&controlListener{Listener: listener}) }()
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
	if err := httpServer.Shutdown(shutdown); err != nil {
		result = errors.Join(result, err)
		_ = httpServer.Close()
	}
	if !dataFinished {
		if err := <-dataDone; err != nil && !errors.Is(err, context.Canceled) {
			result = errors.Join(result, err)
		}
	}
	if !controlFinished {
		if err := <-controlDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, err)
		}
	}
	return result
}
