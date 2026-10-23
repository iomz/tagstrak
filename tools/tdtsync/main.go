// Command tdtsync vendors pinned GS1 Tag Data Translation (TDT) artefacts into
// internal/tdt/tdtdef/artefacts and refreshes artefacts.lock.json.
//
// This is the only part of the repository that performs network access, and it
// is run manually by a maintainer. Neither the build, the tests, nor the code
// generator reach the network: they read the vendored bytes only.
//
// Usage:
//
//	go run ./tools/tdtsync                 # verify vendored files against the lock
//	go run ./tools/tdtsync -update         # re-download and rewrite the lock
//	go run ./tools/tdtsync -update -commit <sha>
//
// Upgrading the pinned commit or the declared TDS version is a deliberate
// change: re-run with -update, review the regenerated Go in the same commit,
// and re-run the golden vectors.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	defaultRepo   = "gs1/TDT"
	defaultCommit = "74d505f65d17ec37755f36c0454cd3c31db15710"
	// defaultTDSVersion is the ratified TDS/TDT revision the pinned artefacts
	// declare. The generator refuses artefacts declaring anything else.
	defaultTDSVersion = "2.2"
	upstreamDir       = "artefacts/JSON"
	lockName          = "artefacts.lock.json"
	vendorDir         = "internal/tdt/tdtdef/artefacts"
)

// vendored lists the upstream files tagstrak depends on. The baseline schemes
// are the only ones v2.0.0 supports; manifest.json is kept for provenance.
// The large GCP prefix-format list is deliberately not vendored: the levels
// that need it (element string, AI JSON, Digital Link) are out of scope.
var vendored = []string{
	"manifest.json",
	"SGTIN-96.json",
	"SSCC-96.json",
	"GIAI-96.json",
	"GRAI-96.json",
}

// reference pins upstream files vendored outside the artefacts directory: the
// GS1 reference translator used to generate committed golden vectors offline,
// and the upstream licence. The translator is a development-time oracle only;
// no Node dependency exists in the build, the tests, or CI.
var reference = map[string]string{
	"demo/TDTtranslator.js": "tools/tdtvectors/vendor/TDTtranslator.js",
	"LICENSE":               "LICENSES/gs1-tdt.txt",
}

// Lock is the pinned provenance record written next to the vendored artefacts.
type Lock struct {
	Repo       string     `json:"repo"`
	Commit     string     `json:"commit"`
	TDSVersion string     `json:"tdsVersion"`
	Retrieved  string     `json:"retrieved"`
	License    string     `json:"license"`
	Files      []LockFile `json:"files"`
}

// LockFile records one vendored artefact and its content digest.
type LockFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func main() {
	update := flag.Bool("update", false, "download artefacts and rewrite the lock file")
	commit := flag.String("commit", defaultCommit, "upstream commit to pin")
	root := flag.String("root", ".", "repository root")
	flag.Parse()

	if err := run(*root, *commit, *update); err != nil {
		fmt.Fprintln(os.Stderr, "tdtsync:", err)
		os.Exit(1)
	}
}

func run(root, commit string, update bool) error {
	dir := filepath.Join(root, vendorDir)
	if update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	lock := Lock{
		Repo:       defaultRepo,
		Commit:     commit,
		TDSVersion: defaultTDSVersion,
		Retrieved:  time.Now().UTC().Format(time.RFC3339),
		License:    "Apache-2.0",
	}
	for _, name := range vendored {
		dest := filepath.Join(dir, name)
		if update {
			body, err := fetch(commit, name)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dest, body, 0o644); err != nil {
				return err
			}
		}
		body, err := os.ReadFile(dest)
		if err != nil {
			return fmt.Errorf("read vendored %s: %w", name, err)
		}
		sum := sha256.Sum256(body)
		lock.Files = append(lock.Files, LockFile{Path: name, SHA256: hex.EncodeToString(sum[:])})
	}
	for upstream, dest := range reference {
		if !update {
			continue
		}
		body, err := fetchPath(commit, upstream)
		if err != nil {
			return err
		}
		out := filepath.Join(root, dest)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, body, 0o644); err != nil {
			return err
		}
	}
	for _, dest := range referenceDests() {
		body, err := os.ReadFile(filepath.Join(root, dest))
		if err != nil {
			return fmt.Errorf("read vendored %s: %w", dest, err)
		}
		sum := sha256.Sum256(body)
		lock.Files = append(lock.Files, LockFile{Path: dest, SHA256: hex.EncodeToString(sum[:])})
	}
	sort.Slice(lock.Files, func(i, j int) bool { return lock.Files[i].Path < lock.Files[j].Path })

	lockPath := filepath.Join(dir, lockName)
	if !update {
		return verify(lockPath, lock)
	}
	body, err := json.MarshalIndent(lock, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(lockPath, append(body, '\n'), 0o644)
}

func verify(path string, got Lock) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var want Lock
	if err := json.Unmarshal(body, &want); err != nil {
		return err
	}
	if want.Commit != got.Commit {
		return fmt.Errorf("lock pins commit %s, requested %s", want.Commit, got.Commit)
	}
	index := make(map[string]string, len(want.Files))
	for _, f := range want.Files {
		index[f.Path] = f.SHA256
	}
	for _, f := range got.Files {
		if index[f.Path] != f.SHA256 {
			return fmt.Errorf("%s: digest %s does not match lock %s", f.Path, f.SHA256, index[f.Path])
		}
		delete(index, f.Path)
	}
	for path := range index {
		return fmt.Errorf("%s: listed in lock but not vendored", path)
	}
	fmt.Printf("tdtsync: %d artefacts verified against %s@%s\n", len(got.Files), want.Repo, want.Commit)
	return nil
}

func referenceDests() []string {
	out := make([]string, 0, len(reference))
	for _, dest := range reference {
		out = append(out, dest)
	}
	sort.Strings(out)
	return out
}

func fetch(commit, name string) ([]byte, error) {
	return fetchPath(commit, upstreamDir+"/"+name)
}

func fetchPath(commit, path string) ([]byte, error) {
	url := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", defaultRepo, commit, path)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
