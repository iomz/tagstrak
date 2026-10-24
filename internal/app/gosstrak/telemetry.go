package gosstrak

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/reader"
	"github.com/iomz/tagstrak/v2/internal/report"
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
	Delivery         []report.Stats  `json:"delivery,omitempty"`
	// Filter is present in adaptive filter mode. It covers the current
	// subscription snapshot, so replacing subscriptions starts it over.
	Filter *filter.AdaptiveStats `json:"filter,omitempty"`
}

// translationCounter is implemented by consumers that translate observations,
// so their counts can be reported without the App knowing how they work.
type translationCounter interface {
	Counts() (translated, untranslatable uint64)
}

// adaptiveStatsSource is implemented along the consumer chain when matching
// may be adaptive.
type adaptiveStatsSource interface {
	AdaptiveStats() (filter.AdaptiveStats, bool)
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
	if a.delivery != nil {
		status.Delivery = a.delivery.Stats()
	}
	if source, ok := a.consumer.(adaptiveStatsSource); ok {
		if st, ok := source.AdaptiveStats(); ok {
			status.Filter = &st
		}
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
		// Destination names are validated to a label-safe character set, and
		// they stand in for addresses, which may carry credentials.
		for _, d := range s.Delivery {
			label := fmt.Sprintf("{destination=%q}", d.Destination)
			_, _ = fmt.Fprintf(w, "gosstrak_report_enqueued_total%s %d\ngosstrak_report_delivered_total%s %d\ngosstrak_report_dropped_total%s %d\ngosstrak_report_failed_total%s %d\ngosstrak_report_undelivered_total%s %d\ngosstrak_report_retries_total%s %d\ngosstrak_report_queue_depth%s %d\ngosstrak_report_queue_capacity%s %d\n",
				label, d.Enqueued, label, d.Delivered, label, d.Dropped, label, d.Failed, label, d.Undelivered, label, d.Retries, label, d.QueueDepth, label, d.QueueCapacity)
		}
		// Candidate names come from the configuration's fixed vocabulary.
		// Counters cover the current subscription snapshot and restart from
		// zero when subscriptions are replaced.
		if f := s.Filter; f != nil {
			_, _ = fmt.Fprintf(w, "gosstrak_filter_adaptive_sampled_total %d\ngosstrak_filter_adaptive_shadows_skipped_total %d\ngosstrak_filter_adaptive_switches_total %d\ngosstrak_filter_adaptive_shadow_seconds_total %g\n",
				f.SampledCalls, f.SkippedShadows, f.Switches, f.ShadowTime.Seconds())
			for _, c := range f.Candidates {
				label := fmt.Sprintf("{candidate=%q}", c.Name)
				_, _ = fmt.Fprintf(w, "gosstrak_filter_adaptive_selected%s %d\ngosstrak_filter_adaptive_samples_total%s %d\ngosstrak_filter_adaptive_mean_cost_seconds%s %g\n",
					label, boolInt(c.Name == f.Selected), label, c.Samples, label, c.MeanCost.Seconds())
			}
		}
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
