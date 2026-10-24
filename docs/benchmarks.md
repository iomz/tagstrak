# Benchmarks

Issue #11 slice 11b compares `v1.0.0` with the current tree on identical inputs.
The [#11 design record](https://github.com/iomz/tagstrak/issues/11#issuecomment-6379991213) is the decision source.
This page records the procedure and the interim results; 11c reruns it after #7 and sets release budgets.

## Procedure

```sh
go run ./tools/benchrun -out results/2026-10-24-interim -count 10 -e2e-runs 10
```

`tools/benchrun` does the following, and never modifies the `v1.0.0` tag:

1. `tools/benchdata` generates the datasets and fails unless their hashes match the committed `benchmarks/datasets.sha256`.
2. `v1.0.0` is checked out into a temporary worktree.
3. `benchmarks/v2bench` runs against the current tree.
4. `benchmarks/v1harness` runs inside the `v1.0.0` worktree through `go test -overlay`, with the same benchmark names and inputs.
5. Both `cmd/gosstrak` binaries are built and driven end to end over a real LLRP socket.
6. `benchstat` compares the raw results, and `meta.json` records the run.

Name the output directory by date and purpose; benchrun refuses to overwrite one.
Run it on one otherwise idle machine.
Results from different machines are not comparable.

### Datasets

The generator is deterministic: PCG seed 20260819, 10,000 SGTIN-96 tags with filter 3 drawn from 2,000 products of 100 companies.

| File | Content |
| --- | --- |
| `tags.txt` | PC and EPC hex per tag |
| `report-{100,500,1000}.bin` | ROAccessReport bodies of that many TagReportData parameters |
| `subs-{100,1000,10000}.v1.csv`, `.v2.txt` | equivalent v1 and v2 subscriptions; half of them, up to 2,000, name products in the tag set |

Each TagReportData holds EPCData followed directly by C1G2-PC, because v1's decoder accepts only that layout.

### Workloads

| Benchmark | v1 | v2 |
| --- | --- | --- |
| `DecodeReport/tags=N` | `llrp.UnmarshalROAccessReportBody` | `llrp.DecodeReadEvents` |
| `Translate/tags=1000` | `tdt.Core.Translate` | `tdt.Translate` and `PureIdentityURI` |
| `TranslateAndMatch/subs=N` | translate, then `Search` on each v1 engine | translate, then the linear matcher |
| `Ingest` (end to end) | `gosstrak start` with an ECSpec file | `gosstrak` with subscriptions only, and with an HTTP destination |

Every matcher reports `matches/op`.
All four v1 engines and v2 find 23, 244, and 1,000 matches per 1,000 tags at 100, 1,000, and 10,000 subscriptions, so the matching workloads are equivalent.

The end-to-end feeder plays the reader: it completes the LLRP handshake, sends 90 ROAccessReports of 1,000 tags, and waits for the keepalive acknowledgement.
v1 processes reports synchronously, so the acknowledgement marks completion.
v2 is complete when its telemetry counts every observation as processed, and the run fails if any observation overflowed or was abandoned.
CPU time and peak resident memory come from the process's rusage.
Latency runs from the moment gosstrak decodes a report, which stamps each observation, to the event's arrival at an HTTP sink.
v1 discards its matches, so only v2 with a destination reports latency.

### Statistics

Each microbenchmark runs 10 times for at least 1 s, and each end-to-end variant runs 10 times.
Tables show the median with a 95% confidence interval, and differences use the Mann–Whitney U test at α = 0.05.
`benchstat` is pinned at `golang.org/x/perf/cmd/benchstat@v0.0.0-20261009192801-be2c69fb417e`.

## Interim results, 2026-10-24

| Field | Value |
| --- | --- |
| Machine | Apple M1 Pro, 10 CPUs, 32 GiB, macOS (Darwin 27.0.0) |
| Go | go1.26.10 darwin/arm64, GOMAXPROCS 10 |
| v1 | `v1.0.0` at `1dfeaf1` |
| v2 | `d060dd8` on `main`, clean tree |
| Dataset manifest SHA-256 | `5da49f52e3609fcbcf040c22b4c1466e5f77135f5cfb9858c95da265a86781ed` |

The run measured `b739546` on `feat/benchmark-baseline`, which `meta.json` records; the rebase merge rewrote it as `d060dd8`, with an identical tree.

### Decode and translate

| Benchmark | v1 | v2 | Change |
| --- | --- | --- | --- |
| DecodeReport, 100 tags | 2.69 µs | 23.3 µs ± 14% | +767% |
| DecodeReport, 1,000 tags | 26.3 µs | 205 µs ± 13% | +681% |
| Translate, 1,000 tags | 472 µs | 686 µs ± 7% | +45% |

v2 allocates more as well: 7,012 against 1,008 allocations per 1,000-tag report, and 417 KiB against 180 KiB per 1,000 translations.
Both are regressions, and neither has been profiled yet.

### Matching

Time to translate 1,000 tags and match them:

| Subscriptions | v1 LegacyEngine | v1 List | v1 PatriciaTrie | v1 SplayTree | v2 linear |
| --- | --- | --- | --- | --- | --- |
| 100 | 18.8 ms | 575 µs | 340 µs | 1.13 ms | 2.06 ms |
| 1,000 | 190 ms | 4.44 ms | 536 µs | 10.5 ms | 19.0 ms |
| 10,000 | 2.17 s | 41.7 ms | 1.12 ms | 11.1 ms | 195 ms |

v2's linear matcher beats only v1's LegacyEngine.
At 10,000 subscriptions it is 174 times slower than v1's PatriciaTrie.
This is the gap #7's candidate matchers must close before release.

### End to end

90,000 tags against 1,000 subscriptions:

| Metric | v1 | v2 | v2 with delivery |
| --- | --- | --- | --- |
| Wall time | 20.4 s | 1.78 s (−91%) | 1.89 s (−91%) |
| CPU time | 28.4 s | 1.92 s (−93%) | 2.30 s (−92%) |
| Throughput | 4,410 tags/s | 50,561 tags/s | 47,728 tags/s |
| Peak RSS | 21.1 MiB | 33.0 MiB (+56%) | 37.6 MiB (+78%) |
| Latency p50 / p95 / p99 | not observable | not observable | 931 ms / 1.76 s / 1.83 s |
| Events delivered | | | 21,960 |

All confidence intervals are within ±2%.

v2 is faster end to end although its decoder, translator, and matcher are each slower than v1's best.
v1 as shipped runs its adaptive EngineFactory, which searches every ready engine for each event, not only the active one, and it logs once per report.
This table compares the shipped products, not the matching algorithms.

Peak memory grows by 56%, and by 78% with delivery; that is a regression.

The feeder sends all 90 reports as fast as the socket accepts them, so delivery latency reflects queueing behind that burst rather than steady-state latency.

### Raw results

The raw `go test` output, end-to-end runs, configurations, benchstat tables, and `meta.json` are kept as release assets, not in the repository.

| Asset | Release | SHA-256 |
| --- | --- | --- |
| [`tagstrak-v1.0.0-baseline-2026-10-24.tar.gz`](https://github.com/iomz/tagstrak/releases/download/v1.0.0/tagstrak-v1.0.0-baseline-2026-10-24.tar.gz): v1 results and `meta.json` | `v1.0.0` | `97068856af6e4d22feb6253d8ee33dd11a5574e862e131aa1bb4d8a6bd295547` |
| `tagstrak-bench-2026-10-24-interim.tar.gz`: the whole run | draft `v2.0.0` | `4dade9e6b7bed3bb6d7b7d95f476e44a60a7d341a6f286c78b049968784732aa` |

The draft is not public; once `v2.0.0` is published, the interim archive is at `https://github.com/iomz/tagstrak/releases/download/v2.0.0/tagstrak-bench-2026-10-24-interim.tar.gz`.
This run predates benchrun keeping the overlay file out of the results, so the archive's `overlay.json` has its local paths replaced by `<v1-worktree>` and `<repo>`.

### Uncertainty

- These are single-machine results from a laptop, not the designated machine 11c will use.
- Decode results from v2 vary by up to ±14% within this run. An earlier run on the same machine, with the same harness and only reporting changes, measured the decode regression at +539% for 1,000 tags instead of +681%. Across runs the variation is larger than the intervals within one run.
- The v1 baseline is measured now from the frozen `v1.0.0` tag, with the current Go toolchain, rather than captured when the tag was made.
- The end-to-end workload stays under v2's queue capacity of 100,000. Under sustained overload v1 slows the reader through TCP backpressure while v2 fails the session by design, so beyond that limit the two do not measure the same thing.

## Side effects

v1 creates `/var/tmp/gosstrak-fc-cache` on the host when it starts, and benchrun does not remove it.
benchrun removes its temporary worktree, binaries, and datasets when it finishes.
An interrupt or SIGTERM stops its child processes and still removes the worktree; only SIGKILL leaves the worktree behind, which `git worktree remove --force <path>` cleans up.
