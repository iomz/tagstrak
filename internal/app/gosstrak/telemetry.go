package gosstrak

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/iomz/tagstrak/v2/internal/reader"
)

type Status struct {
	TelemetryAddress string          `json:"telemetry_address"`
	Healthy          bool            `json:"healthy"`
	Ready            bool            `json:"ready"`
	Draining         bool            `json:"draining"`
	Reader           reader.Snapshot `json:"reader"`
	QueueDepth       int             `json:"queue_depth"`
	QueueCapacity    int             `json:"queue_capacity"`
	Processed        uint64          `json:"processed"`
	Abandoned        uint64          `json:"abandoned"`
	Translated       uint64          `json:"translated"`
	Untranslatable   uint64          `json:"untranslatable"`
}

// translationCounter is implemented by consumers that translate observations,
// so their counts can be reported without the App knowing how they work.
type translationCounter interface {
	Counts() (translated, untranslatable uint64)
}

func (a *App) Status() Status {
	s := a.session.Snapshot()
	address := a.config.TelemetryAddress
	if bound := a.telemetryAddress.Load(); bound != nil {
		address = bound.(string)
	}
	healthy := a.running.Load()
	draining := a.draining.Load()
	status := Status{TelemetryAddress: address, Healthy: healthy, Ready: healthy && !draining && !a.consumerFailed.Load() && s.Ready,
		Draining: draining, Reader: s, QueueDepth: len(a.queue), QueueCapacity: cap(a.queue), Processed: a.processed.Load(), Abandoned: a.abandoned.Load()}
	if counter, ok := a.consumer.(translationCounter); ok {
		status.Translated, status.Untranslatable = counter.Counts()
	}
	return status
}

// Handler exposes read-only JSON health and readiness and Prometheus text
// counters. Bind it only to a trusted operations network; it is not control API.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"/healthz", "/readyz"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			status := a.Status()
			w.Header().Set("Content-Type", "application/json")
			if (r.URL.Path == "/healthz" && !status.Healthy) || (r.URL.Path == "/readyz" && !status.Ready) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			_ = json.NewEncoder(w).Encode(status)
		})
	}
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		s := a.Status()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "gosstrak_ready %d\ngosstrak_reader_attempts_total %d\ngosstrak_reader_reconnects_total %d\ngosstrak_reader_frames_total %d\ngosstrak_reader_reports_total %d\ngosstrak_reader_keepalives_total %d\ngosstrak_reader_failures_total %d\ngosstrak_queue_overflows_total %d\ngosstrak_observations_received_total %d\ngosstrak_observations_processed_total %d\ngosstrak_observations_abandoned_total %d\ngosstrak_observations_translated_total %d\ngosstrak_observations_untranslatable_total %d\ngosstrak_queue_depth %d\ngosstrak_queue_capacity %d\ngosstrak_reader_retry_delay_seconds %g\n",
			boolInt(s.Ready), s.Reader.Attempts, s.Reader.Reconnects, s.Reader.Frames, s.Reader.Reports, s.Reader.Keepalives, s.Reader.Failures, s.Reader.QueueOverflows, s.Reader.Observations, s.Processed, s.Abandoned, s.Translated, s.Untranslatable, s.QueueDepth, s.QueueCapacity, s.Reader.RetryDelay.Seconds())
	})
	return mux
}

// boolInt converts a boolean to its integer representation.
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
