# Tag data translation

Issue #8 rebuilds identifier translation and validation in the v2 milestone tracked by #1.
The design record and its two amendments on #8 are the decision source; #19 is the standards evidence baseline they cite.
This page covers layout, maintenance, verification, and the benchmark baseline.
Supported standards and limitations are stated in the `internal/tdt` package documentation (`go doc ./internal/tdt`), which is the authoritative copy.

## Packages and scope

| Package | Imports | Owns |
| --- | --- | --- |
| `internal/identity` | stdlib | Canonical `Value`, opaque comparable `Key`, components, `Pattern`, error taxonomy |
| `internal/tdt/tdtdef` | stdlib | Generated GS1 scheme tables, provenance, vendored artefacts |
| `internal/tdt/tdtcore` | `identity` | `Translation`, `Encoding`, shared errors, pattern matching |
| `internal/tdt/gs1epc` | `identity`, `tdtcore`, `tdtdef` | GS1 EPC decode/encode, Pure Identity and Tag URI rendering, GS1 pattern syntax |
| `internal/tdt/iso` | `identity`, `tdtcore` | ISO/IEC 15961-2 AFI register, 6-bit text, ISO/IEC 17360 transport-unit interpretation, display-only rendering |
| `internal/tdt` | the above | PC-based recognition, `Translate(Input) (Translation, error)`, `CompilePattern` |

None of these packages imports `llrp`, `internal/reader`, `internal/inventory`, the emulator, or an app package; `internal/tdt/boundary_test.go` enforces that.
They have no third-party dependencies, so #12 can lift them out with a path rewrite.
`internal/app/gosstrak.TranslatingConsumer` adapts `inventory.Observation` to `tdt.Input` at the application boundary.

Supported: SGTIN-96, SSCC-96, GIAI-96, and GRAI-96 from GS1 TDS 2.2, plus ISO/IEC 17360 transport units (AFI `0xA2`, `0xA7`).
ISO 17363 (AFI `0xA9`, `0xAA`) is recognised and refused, because its Gen2 carriage is not established.
Every other scheme returns `ErrUnsupportedScheme`; the remaining evidence gates are listed in #8 Amendment 2.

`cmd/uiigen` now uses these packages instead of its own EPC/ISO helpers.
The duplicate v1 generator in `internal/gosstrak/scheme` and the `internal/gosstrak/test/gendataset.go` command that used it are removed.
The committed datasets in `internal/gosstrak/test/data` stay, because the legacy filtering tests and benchmarks load them.
`internal/gosstrak/tdt` remains only because the legacy filtering engine imports it; #7 replaces both.

## Vendored artefacts and generation

`tools/tdtsync` is the only network-touching step and is run manually.
It pins `gs1/TDT` by commit, stores the files under `internal/tdt/tdtdef/artefacts/`, and records SHA-256 digests in `artefacts.lock.json`.

```sh
go run ./tools/tdtsync                          # verify vendored files against the lock (offline)
go run ./tools/tdtsync -update -commit <sha>    # deliberate upgrade; review regenerated Go in the same commit
go generate ./internal/tdt/...                  # offline and deterministic
```

`go generate` writes `tdtdef/schemes_gen.go`, `tdtdef/provenance_gen.go`, and `iso/registry_gen.go` from vendored bytes only.
CI verifies the digests and fails if regeneration changes any file.
The Apache-2.0 license for the vendored GS1 files is in `LICENSES/gs1-tdt.txt`.

## Golden vectors

Vectors live in `internal/tdt/testdata/vectors/`, and every row carries its provenance.

| File | Tier | Content |
| --- | --- | --- |
| `spec.json` | spec (normative) | Examples transcribed from TDS 2.2, each citing its section |
| `reference.json` | reference oracle | 265 rows generated offline by the pinned GS1 `TDTtranslator.js` |
| `iso.json` | character-table | ISO/IEC 15962 6-bit table, AFI register rows, ISO 6346 check digits (Derived) |
| `negative.json` | negative corpus | One row per enforced rule, each naming the typed error it must produce |
| `patterns.json` | conformance | Match semantics that every #7 matcher must reproduce |

When tiers disagree, `spec` wins and the disagreement is a bug.
Reference vectors are regenerated only when the pinned commit changes, and the output is committed, so Node is never needed in CI or at runtime:

```sh
node tools/tdtvectors/generate.js
```

Property tests cover the binary → Tag URI → binary round trip.
Fuzz targets (`FuzzTranslate`, `FuzzCompilePattern`, `FuzzParseURI`) assert that every input either succeeds or returns a classified error, without panicking.
Differential testing against v1 is deliberately absent; see #8 §7.

## Pattern contract

`tdt.CompilePattern` accepts GS1 `urn:epc:idpat:` (identity level) and `urn:epc:pat:` (encoding level) patterns and returns an immutable `identity.Pattern`.
`internal/filter` (#7) receives compiled patterns, never strings; see [filter.md](filter.md).
Identity-level patterns ignore filter value, partition, and tag length by construction.
`Pattern.Bits` is set only when bit-prefix matching is equivalent to field matching.
That equivalence holds over tags that translate, so matchers test it against `Translation.Bits()` rather than raw reader bytes; numeric maxima and partition validity are not expressible as a mask.
ISO publishes no pattern grammar, so ISO patterns are built with `identity.NewPattern`.

## Benchmark baseline

`internal/tdt/bench_test.go` defines the v2 baseline for representative translation work.
Medians of six runs on Apple M1 Pro, darwin/arm64, Go 1.26.10, recorded 2026-10-23:

| Benchmark | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| `TranslateSGTIN96` | 463.6 | 320 | 8 |
| `TranslateISOTransportUnit` | 455.8 | 176 | 5 |
| `PureIdentityURI` | 201.0 | 104 | 6 |
| `EncodeSGTIN96` | 528.6 | 272 | 11 |
| `CompilePattern` | 653.0 | 480 | 6 |
| `MatchIdentityFields` | 16.7 | 0 | 0 |
| `MatchEncodingBits` | 10.4 | 0 | 0 |

Translation runs once per observation; matching runs once per observation per subscription, so both match paths are allocation-free.
Compare later changes on the same machine with:

```sh
go test -run '^$' -bench . -benchmem -count 6 ./internal/tdt/
```

## Design evaluation

Assumptions: GS1 TDS 2.2 ratified artefacts are the authority for bit layouts; ISO behaviour is limited to what #19 cites.
Generated tables plus reviewed Go rule functions avoid a runtime rule interpreter and runtime regular expressions, at the cost of a generator to maintain.
Taking `tdt.Input` rather than an application observation keeps the packages extractable; the app adapter (`TranslationInput`) is a few lines.
Refusing undocumented ISO layouts surfaces those tags as untranslatable instead of guessing, which narrows ISO coverage until #19 gains primary evidence.

## Verification

```sh
go run ./tools/tdtsync
go generate ./internal/tdt/... && git diff --exit-code
go test ./...
go test -race ./internal/tdt/... ./internal/identity/... ./internal/app/...
go vet ./...
```
