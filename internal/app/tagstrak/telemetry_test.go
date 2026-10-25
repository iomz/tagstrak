package tagstrak

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/report"
)

// fullStatus has every optional part, with two destinations and two adaptive
// candidates, so each labelled family holds several samples.
func fullStatus() Status {
	return Status{
		Ready:    true,
		Delivery: []report.Stats{{Destination: "erp", Delivered: 3}, {Destination: "wms", Delivered: 4}},
		Filter: &filter.AdaptiveStats{Selected: "index", ShadowTime: 1500 * time.Microsecond,
			Candidates: []filter.CandidateStats{{Name: "index", Samples: 9}, {Name: "linear", Samples: 1}}},
	}
}

// TestMetricsExposition checks the Prometheus text format: every family has
// HELP and TYPE lines before its samples, and each family's samples are
// contiguous, which the format requires.
func TestMetricsExposition(t *testing.T) {
	var b strings.Builder
	if err := writeMetrics(&b, fullStatus()); err != nil {
		t.Fatal(err)
	}
	sample := regexp.MustCompile(`^([a-z_]+)(\{[a-z]+="[^"]*"\})? [0-9][0-9.e+-]*$`)
	seen := map[string]bool{}
	current := ""
	for _, line := range strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			name := strings.Fields(line)[2]
			if seen[name] {
				t.Fatalf("family %s appears twice", name)
			}
			seen[name], current = true, ""
		case strings.HasPrefix(line, "# TYPE "):
			fields := strings.Fields(line)
			if fields[3] != "counter" && fields[3] != "gauge" {
				t.Fatalf("type %q", line)
			}
			if strings.HasSuffix(fields[2], "_total") != (fields[3] == "counter") {
				t.Fatalf("%s: counters, and only counters, end in _total", fields[2])
			}
			current = fields[2]
		default:
			m := sample.FindStringSubmatch(line)
			if m == nil || m[1] != current {
				t.Fatalf("sample %q outside its family %q", line, current)
			}
		}
	}
	for _, want := range []string{
		`tagstrak_report_delivered_total{destination="erp"} 3`,
		`tagstrak_report_delivered_total{destination="wms"} 4`,
		`tagstrak_filter_adaptive_selected{candidate="index"} 1`,
		`tagstrak_filter_adaptive_shadow_seconds_total 0.0015`,
	} {
		if !strings.Contains(b.String(), want+"\n") {
			t.Errorf("missing %s in:\n%s", want, b.String())
		}
	}
}

// TestMetricsAreDocumented keeps docs/runtime.md's metrics table and the
// exposed families in step, in both directions.
func TestMetricsAreDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/runtime.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `(tagstrak_[a-z_]+)` \\|").FindAllStringSubmatch(string(doc), -1) {
		documented[m[1]] = true
	}
	for _, f := range metricFamilies(fullStatus()) {
		if !documented[f.name] {
			t.Errorf("%s is exposed but not in the docs/runtime.md metrics table", f.name)
		}
		delete(documented, f.name)
	}
	for name := range documented {
		t.Errorf("%s is documented but not exposed", name)
	}
}
