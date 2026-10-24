// Command benchrun measures v1.0.0 and the current tree on the shared
// datasets and writes raw results, metadata, and benchstat comparisons into
// one directory (#11).
//
// It never modifies the v1.0.0 tag: v1 runs in a temporary worktree, with the
// harness in benchmarks/v1harness added through go test -overlay. Run it on
// one designated, otherwise idle machine; numbers from different machines are
// not comparable.
//
//	go run ./tools/benchrun -out results/2026-10-24-interim
//
// The output directory holds:
//
//	meta.json            hardware, OS, Go, commits, dataset manifest hash, settings
//	v1.txt, v2.txt       raw go test -bench output, -count repetitions
//	v1-e2e.txt ...       black-box end-to-end runs in benchmark format
//	compare-*.txt        benchstat tables
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iomz/tagstrak/v2/llrp"
)

const (
	v1Tag     = "v1.0.0"
	benchstat = "golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e"
)

type settings struct {
	Count      int    `json:"count"`
	Benchtime  string `json:"benchtime"`
	E2EReports int    `json:"e2e_reports"`
	E2ERuns    int    `json:"e2e_runs"`
	E2ESubs    int    `json:"e2e_subscriptions"`
}

func main() {
	out := flag.String("out", "", "results directory to create (required)")
	var s settings
	flag.IntVar(&s.Count, "count", 10, "repetitions per benchmark")
	flag.StringVar(&s.Benchtime, "benchtime", "1s", "go test -benchtime")
	flag.IntVar(&s.E2EReports, "e2e-reports", 90, "ROAccessReports of 1000 tags per end-to-end run; must fit v2's 100000-observation queue")
	flag.IntVar(&s.E2ERuns, "e2e-runs", 5, "end-to-end runs per implementation")
	flag.IntVar(&s.E2ESubs, "e2e-subs", 1000, "subscriptions for end-to-end runs (100, 1000 or 10000)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "benchrun: -out is required")
		os.Exit(2)
	}
	if err := run(*out, s); err != nil {
		fmt.Fprintln(os.Stderr, "benchrun:", err)
		os.Exit(1)
	}
}

func run(out string, s settings) error {
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s exists; results are never overwritten", out)
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	data := filepath.Join(out, "datasets")
	if err := os.MkdirAll(data, 0o755); err != nil {
		return err
	}
	step("generating and verifying datasets")
	if err := command("", nil, os.Stdout, "go", "run", "./tools/benchdata", "-out", data); err != nil {
		return err
	}
	meta, err := metadata(s)
	if err != nil {
		return err
	}

	worktree, err := os.MkdirTemp("", "tagstrak-v1-")
	if err != nil {
		return err
	}
	_ = os.Remove(worktree)
	step("creating the " + v1Tag + " worktree")
	if err := command("", nil, io.Discard, "git", "worktree", "add", "--detach", worktree, v1Tag); err != nil {
		return err
	}
	defer func() { _ = command("", nil, io.Discard, "git", "worktree", "remove", "--force", worktree) }()

	env := []string{"TAGSTRAK_BENCH_DATA=" + data}
	bench := []string{"test", "-run", "^$", "-bench", ".", "-benchmem", "-count", fmt.Sprint(s.Count), "-benchtime", s.Benchtime}

	step("benchmarking v2")
	if err := toFile(filepath.Join(out, "v2.txt"), func(w io.Writer) error {
		return command("", env, w, "go", append(bench, "./benchmarks/v2bench")...)
	}); err != nil {
		return err
	}

	step("benchmarking " + v1Tag)
	overlay, err := writeOverlay(out, worktree)
	if err != nil {
		return err
	}
	if err := toFile(filepath.Join(out, "v1.txt"), func(w io.Writer) error {
		return command(worktree, env, w, "go", append(append(bench, "-overlay", overlay), "./llrp", "./internal/gosstrak/tdt", "./internal/gosstrak/filtering")...)
	}); err != nil {
		return err
	}

	step("building binaries for the end-to-end runs")
	bin := filepath.Join(out, "bin")
	if err := command("", nil, io.Discard, "go", "build", "-o", filepath.Join(bin, "gosstrak-v2"), "./cmd/gosstrak"); err != nil {
		return err
	}
	if err := command(worktree, nil, io.Discard, "go", "build", "-o", filepath.Join(bin, "gosstrak-v1"), "./cmd/gosstrak"); err != nil {
		return err
	}
	step("running end-to-end ingestion")
	if err := endToEnd(out, data, bin, s); err != nil {
		return err
	}

	step("comparing with benchstat")
	if err := compare(out); err != nil {
		return err
	}
	body, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "meta.json"), append(body, '\n'), 0o644); err != nil {
		return err
	}
	_ = os.RemoveAll(bin)
	_ = os.RemoveAll(data)
	step("done: " + out)
	return nil
}

func step(s string) { fmt.Fprintln(os.Stderr, "benchrun:", s) }

func command(dir string, env []string, stdout io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return nil
}

func output(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func toFile(path string, f func(io.Writer) error) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return f(file)
}

// metadata records what #11 requires to interpret a run.
func metadata(s settings) (map[string]any, error) {
	manifest, err := os.ReadFile("benchmarks/datasets.sha256")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(manifest)
	cpu, memory := output("sysctl", "-n", "machdep.cpu.brand_string"), output("sysctl", "-n", "hw.memsize")
	if runtime.GOOS == "linux" {
		cpu = output("sh", "-c", "grep -m1 'model name' /proc/cpuinfo | cut -d: -f2")
		memory = output("sh", "-c", "grep MemTotal /proc/meminfo")
	}
	return map[string]any{
		"recorded_at":      time.Now().UTC().Format(time.RFC3339),
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"kernel":           output("uname", "-sr"),
		"cpu":              cpu,
		"cpus":             runtime.NumCPU(),
		"memory":           memory,
		"go":               output("go", "version"),
		"gomaxprocs":       runtime.GOMAXPROCS(0),
		"v2_commit":        output("git", "rev-parse", "HEAD"),
		"v2_dirty":         output("git", "status", "--porcelain") != "",
		"v1_tag":           v1Tag,
		"v1_commit":        output("git", "rev-parse", v1Tag+"^{commit}"),
		"dataset_manifest": hex.EncodeToString(sum[:]),
		"benchstat":        benchstat,
		"statistics":       "benchstat: median with 95% confidence interval; Mann-Whitney U test, alpha 0.05",
		"settings":         s,
	}, nil
}

func writeOverlay(out, worktree string) (string, error) {
	harness, err := filepath.Abs("benchmarks/v1harness")
	if err != nil {
		return "", err
	}
	replace := map[string]string{
		filepath.Join(worktree, "llrp", "zz_compare_test.go"):                              filepath.Join(harness, "llrp_test.go.txt"),
		filepath.Join(worktree, "internal", "gosstrak", "tdt", "zz_compare_test.go"):       filepath.Join(harness, "tdt_test.go.txt"),
		filepath.Join(worktree, "internal", "gosstrak", "filtering", "zz_compare_test.go"): filepath.Join(harness, "filtering_test.go.txt"),
	}
	body, err := json.Marshal(map[string]any{"Replace": replace})
	if err != nil {
		return "", err
	}
	path := filepath.Join(out, "overlay.json")
	return path, os.WriteFile(path, body, 0o644)
}

// compare writes benchstat tables. Shared names compare v1 with v2 directly;
// the matcher table puts every v1 engine next to v2's linear matcher.
func compare(out string) error {
	tables := []struct {
		name string
		args []string
	}{
		{"compare-decode-translate.txt", []string{"-filter", ".name:/^(DecodeReport|Translate)$/", "v1=" + filepath.Join(out, "v1.txt"), "v2=" + filepath.Join(out, "v2.txt")}},
		{"compare-match.txt", []string{"-filter", ".name:TranslateAndMatch", "-col", "/engine", filepath.Join(out, "v1.txt"), filepath.Join(out, "v2.txt")}},
		{"compare-e2e.txt", []string{"-col", "/impl", filepath.Join(out, "v1-e2e.txt"), filepath.Join(out, "v2-e2e.txt"), filepath.Join(out, "v2-deliver-e2e.txt")}},
	}
	for _, t := range tables {
		if err := toFile(filepath.Join(out, t.name), func(w io.Writer) error {
			return command("", nil, w, "go", append([]string{"run", benchstat}, t.args...)...)
		}); err != nil {
			return err
		}
	}
	return nil
}

// endToEnd runs each gosstrak binary as a black box: one feeder plays the
// reader, streams the same reports as fast as they are accepted, and the
// process's CPU time and peak memory are read from rusage. v1 discards its
// matches, so only v2 can report delivery latency, measured at an HTTP sink.
func endToEnd(out, data, bin string, s settings) error {
	body, err := os.ReadFile(filepath.Join(data, "report-1000.bin"))
	if err != nil {
		return err
	}
	tags := s.E2EReports * 1000
	v2Config := filepath.Join(out, "e2e-v2.toml")
	deliverySink := newSink()
	defer deliverySink.Close()
	if err := writeV2Config(v2Config, filepath.Join(data, fmt.Sprintf("subs-%d.v2.txt", s.E2ESubs)), ""); err != nil {
		return err
	}
	v2DeliverConfig := filepath.Join(out, "e2e-v2-deliver.toml")
	if err := writeV2Config(v2DeliverConfig, filepath.Join(data, fmt.Sprintf("subs-%d.v2.txt", s.E2ESubs)), deliverySink.URL); err != nil {
		return err
	}
	if tags > 100000 {
		return fmt.Errorf("%d tags exceed v2's maximum queue of 100000 observations; v2 fails the session on overflow by design", tags)
	}
	runs := []struct {
		file, impl string
		args       func(reader, telemetry string) []string
		v2         bool
		sink       *sink
	}{
		{"v1-e2e.txt", "v1", func(reader, _ string) []string {
			return []string{filepath.Join(bin, "gosstrak-v1"), "--ecspecfile", filepath.Join(data, fmt.Sprintf("subs-%d.v1.csv", s.E2ESubs)), "--ip", reader, "--managementAddr", "127.0.0.1:0", "start"}
		}, false, nil},
		{"v2-e2e.txt", "v2", func(reader, telemetry string) []string {
			return []string{filepath.Join(bin, "gosstrak-v2"), "--config", v2Config, "--reader", reader, "--telemetry", telemetry}
		}, true, nil},
		{"v2-deliver-e2e.txt", "v2+delivery", func(reader, telemetry string) []string {
			return []string{filepath.Join(bin, "gosstrak-v2"), "--config", v2DeliverConfig, "--reader", reader, "--telemetry", telemetry}
		}, true, deliverySink},
	}
	for _, r := range runs {
		var lines strings.Builder
		fmt.Fprintf(&lines, "goos: %s\ngoarch: %s\npkg: github.com/iomz/tagstrak/v2/e2e\n", runtime.GOOS, runtime.GOARCH)
		for range s.E2ERuns {
			if r.sink != nil {
				r.sink.reset()
			}
			res, err := ingest(body, s.E2EReports, r.args, r.v2)
			if err != nil {
				return fmt.Errorf("%s: %w", r.impl, err)
			}
			fmt.Fprintf(&lines, "BenchmarkIngest/impl=%s/subs=%d/tags=%d 1 %d ns/op %d cpu-ns/op %d peak-rss-bytes %.0f tags/s",
				r.impl, s.E2ESubs, tags, res.wall.Nanoseconds(), res.cpu.Nanoseconds(), res.rss, float64(tags)/res.wall.Seconds())
			if r.sink != nil {
				p50, p95, p99, n := r.sink.percentiles()
				fmt.Fprintf(&lines, " %d p50-latency-ns %d p95-latency-ns %d p99-latency-ns %d delivered", p50.Nanoseconds(), p95.Nanoseconds(), p99.Nanoseconds(), n)
			}
			lines.WriteString("\n")
		}
		if err := os.WriteFile(filepath.Join(out, r.file), []byte(lines.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeV2Config(path, subsFile, sinkURL string) error {
	raw, err := os.ReadFile(subsFile)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("version = 1\n[runtime]\nqueue_capacity = 100000\nshutdown_timeout = \"30s\"\n")
	if sinkURL != "" {
		b.WriteString("[report]\nflush_interval = \"10ms\"\n[[destinations]]\nname = \"sink\"\nurl = \"" + sinkURL + "\"\n")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		id, pattern, _ := strings.Cut(line, " ")
		fmt.Fprintf(&b, "[[subscriptions]]\nid = %q\npatterns = [%q]\n", id, pattern)
		if sinkURL != "" {
			b.WriteString("destinations = [\"sink\"]\n")
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

type result struct {
	wall, cpu time.Duration
	rss       int64
}

// ingest plays the reader for one process: handshake, n reports, then a
// keepalive. v1 processes synchronously, so its acknowledgement marks the end
// of processing; v2 processes asynchronously, so its end is when telemetry
// reports every observation processed. The process is then stopped and its
// rusage read.
func ingest(body []byte, n int, args func(reader, telemetry string) []string, v2 bool) (result, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return result{}, err
	}
	defer listener.Close()
	telemetry, err := freeAddress()
	if err != nil {
		return result{}, err
	}
	argv := args(listener.Addr().String(), telemetry)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return result{}, err
	}
	defer func() { _ = cmd.Process.Kill() }()
	_ = listener.(*net.TCPListener).SetDeadline(time.Now().Add(30 * time.Second))
	conn, err := listener.Accept()
	if err != nil {
		return result{}, fmt.Errorf("process never connected: %w", err)
	}
	defer conn.Close()
	limits := llrp.DefaultLimits()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	if err := llrp.WriteMessage(conn, llrp.ReaderEventNotificationMessage(1, uint64(time.Now().UnixMicro())), limits); err != nil {
		return result{}, err
	}
	request, err := llrp.ReadMessage(conn, limits)
	if err != nil {
		return result{}, fmt.Errorf("handshake: %w", err)
	}
	if err := llrp.WriteMessage(conn, llrp.SetReaderConfigResponseMessage(request.Header.ID), limits); err != nil {
		return result{}, err
	}
	start := time.Now()
	for i := range n {
		if err := llrp.WriteMessage(conn, llrp.ROAccessReportMessage(body, uint32(100+i)), limits); err != nil {
			return result{}, err
		}
	}
	if err := llrp.WriteMessage(conn, llrp.KeepaliveMessage(99999), limits); err != nil {
		return result{}, err
	}
	for {
		m, err := llrp.ReadMessage(conn, limits)
		if err != nil {
			return result{}, fmt.Errorf("waiting for the keepalive acknowledgement: %w", err)
		}
		if m.Header.Type == llrp.KeepaliveAckHeader {
			break
		}
	}
	if v2 {
		if err := awaitProcessed(telemetry, n*1000); err != nil {
			return result{}, err
		}
	}
	wall := time.Since(start)
	// Delivery is asynchronous too; give the sink time to receive the last
	// batches before stopping, then let v2 drain on SIGTERM.
	time.Sleep(200 * time.Millisecond)
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	usage, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok {
		return result{}, errors.New("rusage unavailable on this platform")
	}
	rss := usage.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024 // Linux reports kilobytes, macOS bytes
	}
	cpu := time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
	return result{wall: wall, cpu: cpu, rss: rss}, nil
}

// freeAddress returns a loopback address that was free a moment ago.
func freeAddress() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

// awaitProcessed polls v2's telemetry until it has processed want
// observations, and fails if any were abandoned or the queue overflowed.
func awaitProcessed(telemetry string, want int) error {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://" + telemetry + "/metrics")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			metrics := map[string]float64{}
			for _, line := range strings.Split(string(body), "\n") {
				var name string
				var value float64
				if _, err := fmt.Sscanf(line, "%s %g", &name, &value); err == nil {
					metrics[name] = value
				}
			}
			if metrics["gosstrak_queue_overflows_total"] > 0 || metrics["gosstrak_observations_abandoned_total"] > 0 {
				return errors.New("v2 overflowed its queue or abandoned observations; reduce -e2e-reports")
			}
			if metrics["gosstrak_observations_processed_total"] >= float64(want) {
				return nil
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	return errors.New("v2 did not process every observation within 5 minutes")
}

// sink is an HTTP destination recording each event's delivery latency from
// its observation time.
type sink struct {
	*httptest.Server
	mu        sync.Mutex
	latencies []time.Duration
}

func newSink() *sink {
	s := &sink{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		var batch struct {
			Events []struct {
				ObservedAt time.Time `json:"observedAt"`
			} `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		for _, e := range batch.Events {
			s.latencies = append(s.latencies, now.Sub(e.ObservedAt))
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	return s
}

func (s *sink) reset() {
	s.mu.Lock()
	s.latencies = nil
	s.mu.Unlock()
}

func (s *sink) percentiles() (p50, p95, p99 time.Duration, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := slices.Clone(s.latencies)
	slices.Sort(l)
	if len(l) == 0 {
		return 0, 0, 0, 0
	}
	at := func(q float64) time.Duration { return l[min(len(l)-1, int(q*float64(len(l))))] }
	return at(0.50), at(0.95), at(0.99), len(l)
}
