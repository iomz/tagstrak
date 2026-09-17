package golemu

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/iomz/tagstrak/v2/internal/emulator"
)

// The only mutation is whole-scenario replacement. Validation completes before
// publication; the same endpoint can publish a one-cycle server inventory.
func (a *App) control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.Status())
	})
	mux.HandleFunc("PUT /scenario", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, emulator.MaxScenarioBytes)
		scenario, err := emulator.DecodeScenario(r.Body)
		if err != nil {
			http.Error(w, "invalid or oversized scenario", http.StatusBadRequest)
			return
		}
		if err = r.Context().Err(); err != nil {
			return
		}
		if err = a.server.Replace(scenario); err != nil {
			http.Error(w, "invalid scenario", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// Limit control to four active requests/connections with no waiting queue.
type controlListener struct {
	net.Listener
	active atomic.Int32
}

func (l *controlListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.active.Add(1) > 4 {
			l.active.Add(-1)
			_ = c.Close()
			continue
		}
		return &controlConn{Conn: c, release: func() { l.active.Add(-1) }}, nil
	}
}

type controlConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *controlConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
