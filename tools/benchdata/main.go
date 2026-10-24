// Command benchdata generates the shared benchmark datasets that the v1 and v2
// harnesses read, so both sides measure identical bytes (#11).
//
// The datasets are deterministic and are not committed; only their SHA-256
// manifest is (benchmarks/datasets.sha256). Generation refuses to finish if a
// file's hash differs from the manifest, so a changed generator cannot
// silently change a baseline.
//
// Usage:
//
//	go run ./tools/benchdata -out DIR            # generate and verify
//	go run ./tools/benchdata -out DIR -update    # generate and rewrite the manifest
//
// Datasets:
//
//	tags.txt              "PC EPC" hex per line, SGTIN-96, filter 3
//	report-N.bin          ROAccessReport bodies of N TagReportData parameters,
//	                      each EPCData followed by C1G2-PC, the layout v1 decodes
//	subs-N.v1.csv         v1 ECSpec rows: report URI, then a urn:epc:pat prefix
//	subs-N.v2.txt         "ID pattern" lines with the equivalent v2 pattern
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iomz/tagstrak/v2/internal/tdt"
)

const (
	seed      = 20260819 // the v1.0.0 tag date, fixed forever
	tagCount  = 10000
	products  = 2000
	companies = 100
	pcWord    = 0x3000 // 96-bit EPC, GS1 toggle
	manifest  = "benchmarks/datasets.sha256"
)

var (
	reportSizes = []int{100, 500, 1000}
	subsSizes   = []int{100, 1000, 10000}
)

type product struct{ company, item string }

func main() {
	out := flag.String("out", "", "directory to write the datasets into (required)")
	update := flag.Bool("update", false, "rewrite "+manifest+" instead of verifying against it")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "benchdata: -out is required")
		os.Exit(2)
	}
	if err := run(*out, *update); err != nil {
		fmt.Fprintln(os.Stderr, "benchdata:", err)
		os.Exit(1)
	}
}

func run(out string, update bool) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	files, err := generate()
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(out, name), body, 0o644); err != nil {
			return err
		}
		names = append(names, name)
	}
	slices.Sort(names)
	var sums strings.Builder
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	if update {
		return os.WriteFile(manifest, []byte(sums.String()), 0o644)
	}
	want, err := os.ReadFile(manifest)
	if err != nil {
		return fmt.Errorf("read manifest (run from the repository root): %w", err)
	}
	if string(want) != sums.String() {
		return errors.New("generated datasets differ from " + manifest + "; a baseline must not change silently (use -update only when the change is intended)")
	}
	fmt.Printf("benchdata: %d datasets in %s match %s\n", len(names), out, manifest)
	return nil
}

func generate() (map[string][]byte, error) {
	r := rand.New(rand.NewPCG(seed, seed))
	digits := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('0' + r.IntN(10))
		}
		return string(b)
	}
	companyPool := make([]string, companies)
	for i := range companyPool {
		companyPool[i] = digits(7)
	}
	pool := make([]product, 0, products)
	used := map[product]bool{}
	for len(pool) < products {
		p := product{company: companyPool[r.IntN(companies)], item: digits(6)}
		if !used[p] {
			used[p] = true
			pool = append(pool, p)
		}
	}

	files := map[string][]byte{}
	type tag struct {
		pc  uint16
		epc []byte
	}
	tags := make([]tag, tagCount)
	var lines strings.Builder
	for i := range tags {
		p := pool[r.IntN(products)]
		uri := fmt.Sprintf("urn:epc:id:sgtin:%s.%s.%d", p.company, p.item, r.Int64N(274877906944))
		v, err := tdt.ParsePureIdentityURI(uri)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", uri, err)
		}
		tr, err := tdt.EncodeGS1(v, "sgtin-96", 3)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", uri, err)
		}
		bits, _ := tr.Encoding.Bits()
		tags[i] = tag{pc: pcWord, epc: bits}
		fmt.Fprintf(&lines, "%04x %x\n", pcWord, bits)
	}
	files["tags.txt"] = []byte(lines.String())

	for _, n := range reportSizes {
		var body []byte
		for _, t := range tags[:n] {
			body = append(body, tagReportData(t.epc, t.pc)...)
		}
		files[fmt.Sprintf("report-%d.bin", n)] = body
	}

	for _, n := range subsSizes {
		var v1, v2 strings.Builder
		chosen := map[product]bool{}
		hits := min(n/2, products)
		for i := range n {
			var p product
			for {
				if i < hits {
					p = pool[r.IntN(products)]
				} else {
					p = product{company: companyPool[r.IntN(companies)], item: digits(6)}
					if used[p] {
						continue
					}
				}
				if !chosen[p] {
					break
				}
			}
			chosen[p] = true
			fmt.Fprintf(&v1, "http://dest/s%05d,urn:epc:pat:sgtin-96:3.%s.%s\n", i, p.company, p.item)
			fmt.Fprintf(&v2, "s%05d urn:epc:pat:sgtin-96:3.%s.%s.*\n", i, p.company, p.item)
		}
		files[fmt.Sprintf("subs-%d.v1.csv", n)] = []byte(v1.String())
		files[fmt.Sprintf("subs-%d.v2.txt", n)] = []byte(v2.String())
	}
	return files, nil
}

// tagReportData encodes one TagReportData parameter holding EPCData and a
// C1G2-PC TV parameter, nothing else: v1's decoder assumes exactly this.
func tagReportData(epc []byte, pc uint16) []byte {
	epcData := make([]byte, 6+len(epc))
	binary.BigEndian.PutUint16(epcData[0:2], 241)
	binary.BigEndian.PutUint16(epcData[2:4], uint16(len(epcData)))
	binary.BigEndian.PutUint16(epcData[4:6], uint16(len(epc)*8))
	copy(epcData[6:], epc)
	pcParam := []byte{0x80 | 12, byte(pc >> 8), byte(pc)}
	trd := make([]byte, 4, 4+len(epcData)+len(pcParam))
	binary.BigEndian.PutUint16(trd[0:2], 240)
	binary.BigEndian.PutUint16(trd[2:4], uint16(4+len(epcData)+len(pcParam)))
	return append(append(trd, epcData...), pcParam...)
}
