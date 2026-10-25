# Quality gates

Issue #11 slice 11a defines the checks every change passes before merge.
The [#11 design record](https://github.com/iomz/tagstrak/issues/11#issuecomment-6379991213) is the decision source; the [benchmark baseline](benchmarks.md) (11b) and release (11c) follow.

## CI

`.github/workflows/test.yml` runs on every pull request to `main`, on Go 1.26.10 and 1.27.3:

| Step | Command |
| --- | --- |
| Formatting | `gofmt -l .` must print nothing |
| Module files | `go mod tidy -diff` |
| TDT artefacts | `go run ./tools/tdtsync`, then `go generate ./internal/tdt/...` with no diff |
| Vet | `go vet ./...` |
| Static analysis (Go 1.26.10 only) | `go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...` |
| Vulnerability scan | `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...` |
| Tests | `go test -race ./...` |

The analysis tools are pinned by version and run with `go run`, so they stay out of `go.mod`.
`staticcheck` runs on one toolchain because v0.8.1 cannot read Go 1.27 export data; the vulnerability scan runs on both, because standard-library vulnerabilities depend on the Go release.
Bump either pin deliberately, after the new release has been out for a while.

## Leak checks

Every package that runs servers, workers, or applications has a `TestMain` with `goleak.VerifyTestMain`: `internal/app/tagstrak`, `internal/app/golemu`, `internal/control`, `internal/report`, `internal/report/httpreport`, `internal/reader`, `internal/emulator`, `internal/e2e`, `cmd/tagstrak`, and `cmd/golemu`.
A goroutine that outlives the package's tests fails them.
Tests also check that control and telemetry listeners stop accepting and that Unix sockets are removed after `Run`.

## Fuzzing

| Input | Targets |
| --- | --- |
| LLRP frames and parameters | `llrp.FuzzDecodeMessageNeverPanics`, `FuzzDecodeParametersNeverPanics`, `FuzzDecodeReadEventsNeverPanics` |
| Configuration and state files | `config.FuzzDecodeTOML`, `FuzzDecodeJSON`, `FuzzDecodeState` |
| Inventory files | `inventory.FuzzDecodeInventoryJSON` |
| golemu scenarios | `emulator.FuzzDecodeScenario` |
| Control request bodies | `control.FuzzReadJSON` |
| Patterns and identifiers | `tdt.FuzzCompilePattern`, `FuzzParseURI`, `FuzzTranslate` |
| Matcher equivalence | `filter.FuzzIndexAgreesWithLinear` |

Seeds include malformed inputs and inputs exactly at and one byte over each size limit.
`go test` runs every seed in CI; there is no scheduled fuzzing.
When a parser changes, fuzz it by hand, for example:

```sh
go test -run '^$' -fuzz '^FuzzDecodeTOML$' -fuzztime 2m ./internal/config
```

Commit any input that finds a bug under the package's `testdata/fuzz/` so it becomes a permanent seed.

## End-to-end scenarios

`internal/e2e` runs golemu and tagstrak in-process over real sockets, building tagstrak through the production configuration path:

| Test | Proves |
| --- | --- |
| `TestGolemuToHTTPDestination` | handshake, translation, matching, and delivery, then a clean drain |
| `TestDestinationOutageThenRecovery` | retries carry deliveries through an outage without loss |
| `TestReaderReconnect` | tagstrak reconnects to a replaced reader and delivers no event twice |
| `TestScenarioReplacedThroughGolemuControl` | a scenario replaced over golemu's control socket reaches the destination |
| `TestSubscriptionsReplacedThroughAPI` | subscriptions replaced over the management API route live and persist |
| `TestShutdownWithBatchesInFlight` | shutdown meets its deadline and reports undelivered events |
