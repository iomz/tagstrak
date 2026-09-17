package emulator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
)

// Status is a race-safe operational snapshot; counters span all connections.
type Status struct {
	Address    string `json:"address"`
	Running    bool   `json:"running"`
	Clients    int    `json:"clients"`
	Accepted   uint64 `json:"accepted"`
	Rejected   uint64 `json:"rejected"`
	Failures   uint64 `json:"failures"`
	Cycles     uint64 `json:"cycles"`
	Reports    uint64 `json:"reports"`
	Keepalives uint64 `json:"keepalives"`
	Revision   uint64 `json:"revision"`
}

type Server struct {
	config  Config
	catalog *Catalog
	clock   Clock
	random  RandomFactory
	logger  *slog.Logger
	started atomic.Bool
	mu      sync.Mutex
	status  Status
}

// New performs no I/O and starts no workers. The clock is shared; random factory
// calls must be safe concurrently and return a fresh private generator each time.
func New(config Config, scenario *Scenario, clock Clock, random RandomFactory, logger *slog.Logger) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if scenario == nil || len(scenario.cycles) == 0 {
		return nil, ErrScenario
	}
	if clock == nil || random == nil {
		return nil, fmt.Errorf("%w: clock and random factory required", ErrConfig)
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{config: config, catalog: newCatalog(scenario), clock: clock, random: random, logger: logger, status: Status{Address: config.Address}}, nil
}

// Status returns a race-safe operational snapshot with the current scenario
// revision.
func (s *Server) Status() Status {
	s.mu.Lock()
	v := s.status
	s.mu.Unlock()
	_, v.Revision = s.catalog.snapshot()
	return v
}
func (s *Server) update(f func(*Status)) { s.mu.Lock(); defer s.mu.Unlock(); f(&s.status) }

// Replace publishes a validated scenario. In-flight cycles finish their current
// immutable snapshot; each client resets its cursor at its next cycle boundary.
func (s *Server) Replace(scenario *Scenario) error {
	if scenario == nil || len(scenario.cycles) == 0 {
		return ErrScenario
	}
	s.catalog.replace(scenario)
	return nil
}

// Run owns the listener, cancellation watcher, and every client worker. Client
// errors are isolated; listener failure ends the run and cancels all clients.
func (s *Server) Run(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.config.Address)
	if err != nil {
		return err
	}
	s.update(func(v *Status) { v.Address = listener.Addr().String(); v.Running = true })
	s.logger.Info("emulator listening", "address", listener.Addr().String())
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); <-ctx.Done(); _ = listener.Close() }()
	var workers sync.WaitGroup
	defer func() {
		cancel()
		_ = listener.Close()
		workers.Wait()
		<-watchDone
		s.update(func(v *Status) { v.Running = false })
	}()
	slots := make(chan struct{}, s.config.MaxClients)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			s.update(func(v *Status) { v.Rejected++ })
			continue
		}
		s.update(func(v *Status) { v.Clients++; v.Accepted++ })
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-slots; s.update(func(v *Status) { v.Clients-- }) }()
			if err := s.serve(ctx, conn); err != nil && ctx.Err() == nil && !errors.Is(err, io.EOF) {
				s.update(func(v *Status) { v.Failures++ })
				s.logger.Warn("emulator session closed", "peer", conn.RemoteAddr().String(), "error", err)
			}
		}()
	}
}
