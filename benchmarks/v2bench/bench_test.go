// Package v2bench measures v2 on the shared datasets from tools/benchdata.
// benchmarks/v1harness holds the same benchmarks for v1.0.0; tools/benchrun
// runs both and compares them (#11). Benchmarks skip unless
// TAGSTRAK_BENCH_DATA names a generated dataset directory.
package v2bench

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/filter"
	"github.com/iomz/tagstrak/v2/internal/identity"
	"github.com/iomz/tagstrak/v2/internal/tdt"
	"github.com/iomz/tagstrak/v2/llrp"
)

const translateTags = 1000

func dataDir(b *testing.B) string {
	b.Helper()
	dir := os.Getenv("TAGSTRAK_BENCH_DATA")
	if dir == "" {
		b.Skip("TAGSTRAK_BENCH_DATA is not set; run go run ./tools/benchdata first")
	}
	return dir
}

type tag struct {
	pc  uint16
	epc []byte
}

func loadTags(b *testing.B, n int) []tag {
	b.Helper()
	f, err := os.Open(filepath.Join(dataDir(b), "tags.txt"))
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	var tags []tag
	scanner := bufio.NewScanner(f)
	for scanner.Scan() && len(tags) < n {
		pcHex, epcHex, _ := strings.Cut(scanner.Text(), " ")
		pc, err := strconv.ParseUint(pcHex, 16, 16)
		if err != nil {
			b.Fatal(err)
		}
		epc, err := hex.DecodeString(epcHex)
		if err != nil {
			b.Fatal(err)
		}
		tags = append(tags, tag{pc: uint16(pc), epc: epc})
	}
	return tags
}

func BenchmarkDecodeReport(b *testing.B) {
	for _, n := range []int{100, 500, 1000} {
		body, err := os.ReadFile(filepath.Join(dataDir(b), fmt.Sprintf("report-%d.bin", n)))
		if err != nil {
			b.Fatal(err)
		}
		if events, err := llrp.DecodeReadEvents(body, llrp.DefaultLimits()); err != nil || len(events) != n {
			b.Fatalf("report-%d decodes to %d events: %v", n, len(events), err)
		}
		b.Run(fmt.Sprintf("tags=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for range b.N {
				events, _ := llrp.DecodeReadEvents(body, llrp.DefaultLimits())
				_ = events
			}
		})
	}
}

// BenchmarkTranslate renders each tag's EPC Pure Identity URI, which is what
// v1's Core.Translate returns, so both sides produce the same output.
func BenchmarkTranslate(b *testing.B) {
	tags := loadTags(b, translateTags)
	translator := tdt.New()
	translate := func(t tag) (string, error) {
		tr, err := translator.Translate(tdt.Input{Bits: t.epc, BitLen: len(t.epc) * 8, PC: t.pc, HasPC: true})
		if err != nil {
			return "", err
		}
		return tdt.PureIdentityURI(tr.Identity)
	}
	for _, t := range tags {
		if _, err := translate(t); err != nil {
			b.Fatalf("%x: %v", t.epc, err)
		}
	}
	b.Run(fmt.Sprintf("tags=%d", translateTags), func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			for _, t := range tags {
				_, _ = translate(t)
			}
		}
	})
}

// BenchmarkTranslateAndMatch is the per-tag pipeline cost v1's Engine.Search
// covers: translate, then match against every subscription.
func BenchmarkTranslateAndMatch(b *testing.B) {
	tags := loadTags(b, translateTags)
	translator := tdt.New()
	for _, n := range []int{100, 1000, 10000} {
		for _, engine := range []struct {
			name    string
			compile filter.Compiler
		}{
			{"index", filter.CompileIndex}, // the v2 default
			{"linear", filter.CompileLinear},
		} {
			benchmarkTranslateAndMatch(b, translator, tags, n, engine.name, loadMatcher(b, n, engine.compile))
		}
	}
}

// benchmarkTranslateAndMatch translates and matches every tag per op.
func benchmarkTranslateAndMatch(b *testing.B, translator *tdt.Translator, tags []tag, n int, engine string, m filter.Matcher) {
	matches := 0
	buf := make([]filter.ID, 0, 4)
	match := func(t tag) int {
		tr, err := translator.Translate(tdt.Input{Bits: t.epc, BitLen: len(t.epc) * 8, PC: t.pc, HasPC: true})
		if err != nil {
			return 0
		}
		buf = m.Match(filter.Subject{Identity: tr.Identity, Scheme: tr.Encoding.Scheme, Fields: tr.Encoding.RawFields(), Bits: tr.Encoding.RawBits(), BitLen: tr.Encoding.BitLen}, buf[:0])
		return len(buf)
	}
	for _, t := range tags {
		if match(t) > 0 {
			matches++
		}
	}
	// The engine key lines this up with v1's engines in one benchstat table.
	b.Run(fmt.Sprintf("engine=%s/subs=%d", engine, n), func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			for _, t := range tags {
				match(t)
			}
		}
		b.ReportMetric(float64(matches), "matches/op")
	})
}

func loadMatcher(b *testing.B, n int, compile filter.Compiler) filter.Matcher {
	b.Helper()
	f, err := os.Open(filepath.Join(dataDir(b), fmt.Sprintf("subs-%d.v2.txt", n)))
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	var subs []filter.Subscription
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		id, text, _ := strings.Cut(scanner.Text(), " ")
		p, err := tdt.CompilePattern(text)
		if err != nil {
			b.Fatalf("%s: %v", text, err)
		}
		subs = append(subs, filter.Subscription{ID: filter.ID(id), Patterns: []identity.Pattern{p}})
	}
	m, err := compile(subs)
	if err != nil {
		b.Fatal(err)
	}
	return m
}
