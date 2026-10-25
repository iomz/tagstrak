package tagstrak

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_ = writeMetrics(w, a.Status())
	})
	return mux
}

// metricFamily is one Prometheus metric family. The text format requires a
// family's samples to be contiguous, so samples for every destination or
// candidate are gathered into their family before anything is written.
type metricFamily struct {
	name, kind, help string
	samples          []metricSample
}

type metricSample struct {
	labels, value string
}

func one(value string) []metricSample { return []metricSample{{value: value}} }

func count[T ~uint64 | ~int | ~int64 | ~uint32](v T) string { return strconv.FormatInt(int64(v), 10) }

func seconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }

// metricFamilies lists every family /metrics exposes, in output order.
// docs/runtime.md documents each one; TestMetricsAreDocumented keeps the two
// in step.
func metricFamilies(s Status) []metricFamily {
	families := []metricFamily{
		{"tagstrak_ready", "gauge", "1 when the reader session is serving, else 0.", one(count(boolInt(s.Ready)))},
		{"tagstrak_reader_attempts_total", "counter", "Reader connection attempts.", one(count(s.Reader.Attempts))},
		{"tagstrak_reader_reconnects_total", "counter", "Reader reconnections after a lost session.", one(count(s.Reader.Reconnects))},
		{"tagstrak_reader_frames_total", "counter", "LLRP frames read from the reader.", one(count(s.Reader.Frames))},
		{"tagstrak_reader_reports_total", "counter", "RO_ACCESS_REPORT messages read from the reader.", one(count(s.Reader.Reports))},
		{"tagstrak_reader_keepalives_total", "counter", "Keepalives acknowledged.", one(count(s.Reader.Keepalives))},
		{"tagstrak_reader_failures_total", "counter", "Reader connection or session failures.", one(count(s.Reader.Failures))},
		{"tagstrak_reader_retry_delay_seconds", "gauge", "Current reconnect delay, 0 while connected.", one(seconds(s.Reader.RetryDelay))},
		{"tagstrak_queue_overflows_total", "counter", "Observations refused by a full queue; any overflow fails the run.", one(count(s.Reader.QueueOverflows))},
		{"tagstrak_queue_depth", "gauge", "Observations waiting for processing.", one(count(s.QueueDepth))},
		{"tagstrak_queue_capacity", "gauge", "Observation queue capacity.", one(count(s.QueueCapacity))},
		{"tagstrak_observations_received_total", "counter", "Tag observations decoded from reader reports.", one(count(s.Reader.Observations))},
		{"tagstrak_observations_processed_total", "counter", "Observations translated and matched.", one(count(s.Processed))},
		{"tagstrak_observations_abandoned_total", "counter", "Observations not processed: still queued at the shutdown deadline, or failed in processing.", one(count(s.Abandoned))},
		{"tagstrak_observations_translated_total", "counter", "Observations translated to an identity.", one(count(s.Translated))},
		{"tagstrak_observations_untranslatable_total", "counter", "Observations in schemes tagstrak does not support.", one(count(s.Untranslatable))},
	}

	// Destination names are validated to a label-safe character set, and
	// they stand in for addresses, which may carry credentials.
	report := []metricFamily{
		{name: "tagstrak_report_enqueued_total", kind: "counter", help: "Report events accepted into a destination queue."},
		{name: "tagstrak_report_delivered_total", kind: "counter", help: "Report events delivered."},
		{name: "tagstrak_report_dropped_total", kind: "counter", help: "Report events dropped by a full destination queue."},
		{name: "tagstrak_report_failed_total", kind: "counter", help: "Report events in batches that finally failed."},
		{name: "tagstrak_report_undelivered_total", kind: "counter", help: "Report events still queued at the shutdown deadline."},
		{name: "tagstrak_report_retries_total", kind: "counter", help: "Delivery retries."},
		{name: "tagstrak_report_queue_depth", kind: "gauge", help: "Report events waiting for delivery."},
		{name: "tagstrak_report_queue_capacity", kind: "gauge", help: "Destination queue capacity."},
	}
	for _, d := range s.Delivery {
		label := fmt.Sprintf("{destination=%q}", d.Destination)
		for i, v := range []uint64{d.Enqueued, d.Delivered, d.Dropped, d.Failed, d.Undelivered, d.Retries, uint64(d.QueueDepth), uint64(d.QueueCapacity)} {
			report[i].samples = append(report[i].samples, metricSample{label, count(v)})
		}
	}
	families = append(families, report...)

	// Candidate names come from the configuration's fixed vocabulary.
	// Counters cover the current subscription snapshot and restart from zero
	// when subscriptions are replaced.
	if f := s.Filter; f != nil {
		families = append(families,
			metricFamily{"tagstrak_filter_adaptive_sampled_total", "counter", "Adaptive matcher calls sampled for measurement.", one(count(f.SampledCalls))},
			metricFamily{"tagstrak_filter_adaptive_shadows_skipped_total", "counter", "Shadow evaluations skipped by the overhead budget.", one(count(f.SkippedShadows))},
			metricFamily{"tagstrak_filter_adaptive_switches_total", "counter", "Adaptive matcher switches.", one(count(f.Switches))},
			metricFamily{"tagstrak_filter_adaptive_shadow_seconds_total", "counter", "Time spent in shadow evaluations.", one(seconds(f.ShadowTime))},
		)
		candidate := []metricFamily{
			{name: "tagstrak_filter_adaptive_selected", kind: "gauge", help: "1 for the candidate matcher currently selected."},
			{name: "tagstrak_filter_adaptive_samples_total", kind: "counter", help: "Measurements per candidate matcher."},
			{name: "tagstrak_filter_adaptive_mean_cost_seconds", kind: "gauge", help: "Mean measured cost per candidate matcher."},
		}
		for _, c := range f.Candidates {
			label := fmt.Sprintf("{candidate=%q}", c.Name)
			candidate[0].samples = append(candidate[0].samples, metricSample{label, count(boolInt(c.Name == f.Selected))})
			candidate[1].samples = append(candidate[1].samples, metricSample{label, count(c.Samples)})
			candidate[2].samples = append(candidate[2].samples, metricSample{label, seconds(c.MeanCost)})
		}
		families = append(families, candidate...)
	}
	return families
}

// writeMetrics writes s in the Prometheus text exposition format. Families
// without samples, such as report families with no destinations, are left out.
func writeMetrics(w io.Writer, s Status) error {
	var b strings.Builder
	for _, f := range metricFamilies(s) {
		if len(f.samples) == 0 {
			continue
		}
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		for _, sample := range f.samples {
			fmt.Fprintf(&b, "%s%s %s\n", f.name, sample.labels, sample.value)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// boolInt converts a boolean to its integer representation.
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
