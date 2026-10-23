package tdtdef_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iomz/tagstrak/v2/internal/tdt/tdtdef"
)

type lock struct {
	Repo       string `json:"repo"`
	Commit     string `json:"commit"`
	TDSVersion string `json:"tdsVersion"`
	Files      []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

func readLock(t *testing.T) lock {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("artefacts", "artefacts.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	var lk lock
	if err := json.Unmarshal(body, &lk); err != nil {
		t.Fatal(err)
	}
	return lk
}

// TestVendoredArtefactsMatchLock verifies the pinned artefacts have not
// drifted. Generation reads these bytes and nothing else, so this is the
// integrity check for the whole generated layer.
func TestVendoredArtefactsMatchLock(t *testing.T) {
	lk := readLock(t)
	if len(lk.Files) == 0 {
		t.Fatal("lock lists no files")
	}
	for _, f := range lk.Files {
		path := filepath.Join("artefacts", f.Path)
		if filepath.IsAbs(f.Path) || filepath.Dir(f.Path) != "." {
			// Reference-oracle files live outside this package.
			path = filepath.Join("..", "..", "..", f.Path)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s: %v", f.Path, err)
			continue
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			t.Errorf("%s: digest %s does not match lock %s", f.Path, got, f.SHA256)
		}
	}
}

// TestProvenanceMatchesLock checks the generated provenance is the lock.
func TestProvenanceMatchesLock(t *testing.T) {
	lk := readLock(t)
	p := tdtdef.Provenance
	if p.Commit != lk.Commit || p.TDSVersion != lk.TDSVersion || p.Repo != lk.Repo {
		t.Fatalf("provenance %+v does not match lock %+v", p, lk)
	}
}

// TestArtefactsDeclarePinnedTDSVersion is the artefact-skew guard: an upstream
// file that declares a different revision must not be generated silently.
func TestArtefactsDeclarePinnedTDSVersion(t *testing.T) {
	lk := readLock(t)
	for _, f := range lk.Files {
		if filepath.Dir(f.Path) != "." || filepath.Ext(f.Path) != ".json" || f.Path == "artefacts.lock.json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join("artefacts", f.Path))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			TDT struct {
				EPCTDSVersion string `json:"epcTDSVersion"`
			} `json:"tdt:epcTagDataTranslation"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.TDT.EPCTDSVersion == "" {
			continue // manifest.json carries no scheme
		}
		if doc.TDT.EPCTDSVersion != lk.TDSVersion {
			t.Errorf("%s declares TDS %s, lock pins %s", f.Path, doc.TDT.EPCTDSVersion, lk.TDSVersion)
		}
	}
}

// TestGenerationIsReproducible regenerates into a scratch tree and compares
// byte for byte, so a committed generated file can never drift from the
// artefacts it claims to come from.
func TestGenerationIsReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generation in short mode")
	}
	root := t.TempDir()
	dest := filepath.Join(root, "internal", "tdt", "tdtdef", "artefacts")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("artefacts")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join("artefacts", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, e.Name()), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command("go", "run", "./internal/tdt/tdtdef/cmd/gentdt", "-root", root)
	cmd.Dir = filepath.Join("..", "..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gentdt: %v\n%s", err, out)
	}

	for _, name := range []string{
		filepath.Join("schemes_gen.go"),
		filepath.Join("provenance_gen.go"),
		filepath.Join("testdata", "binary_patterns.json"),
	} {
		want, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(root, "internal", "tdt", "tdtdef", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from a fresh generation; run go generate ./internal/tdt/...", name)
		}
	}
}

// TestLayoutCoversTagLength checks every generated option describes a complete
// encoding, with no overlapping or missing bits.
func TestLayoutCoversTagLength(t *testing.T) {
	for _, s := range tdtdef.Schemes {
		for _, opt := range s.Options {
			covered := make([]bool, s.TagLength)
			mark := func(offset, length int) {
				for i := 0; i < length; i++ {
					if offset+i >= s.TagLength {
						t.Fatalf("%s option %d: bit %d is outside the tag", s.Name, opt.GCPLength, offset+i)
					}
					if covered[offset+i] {
						t.Fatalf("%s option %d: bit %d is covered twice", s.Name, opt.GCPLength, offset+i)
					}
					covered[offset+i] = true
				}
			}
			for _, l := range opt.BinaryLiterals {
				mark(l.BitOffset, l.BitLength)
			}
			for _, f := range opt.BinaryFields {
				mark(f.BitOffset, f.BitLength)
			}
			for i, ok := range covered {
				if !ok {
					t.Fatalf("%s option %d: bit %d is unspecified", s.Name, opt.GCPLength, i)
				}
			}
			if opt.Partition != 12-opt.GCPLength {
				t.Errorf("%s option %d: partition %d", s.Name, opt.GCPLength, opt.Partition)
			}
		}
	}
}
