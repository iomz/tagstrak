# Subscription matching

Issue #7 rebuilds filtering in the v2 milestone tracked by #1.
This page covers slices 1 to 3 from the [#7 design records](https://github.com/iomz/tagstrak/issues/7): the matcher contract, atomic replacement, the linear reference matcher, the prefix index that is the default, and the experimental adaptive mode.
Benchmarks of adaptive overhead, switch stability, and selection regret remain open on #7.

## Packages and scope

| Package | Imports | Owns |
| --- | --- | --- |
| `internal/filter` | `identity` | `Subject`, `Subscription`, `Matcher`, `Compiler`, `Compile`, `CompileIndex`, `CompileLinear`, `CompileAdaptive`, `Set` |
| `internal/app/gosstrak` | `filter`, `tdt`, `report` | `SubjectOf` adapter, `MatchingProcessor`, routing to delivery |

`internal/filter` never parses pattern text and never imports the translator; `TestFilterDependsOnlyOnIdentity` enforces that.
Patterns are compiled by `tdt.CompilePattern` ([tdt.md](tdt.md)), so malformed text is rejected before filtering sees it.
`SubjectOf` builds a `filter.Subject` from a translation's raw fields and bits without copying, mirroring how `TranslationInput` adapts observations on the way in.

## Contract

```go
m, err := filter.Compile([]filter.Subscription{ // the prefix index
	{ID: "product", Patterns: []identity.Pattern{p1, p2}},
})
ids := m.Match(subject, buf[:0])
```

- A subscription matches when any of its patterns matches. Exclusion patterns are deferred until a consumer needs them.
- `Match` appends IDs to a caller-owned buffer in subscription order, each at most once, and does not allocate.
- Identity-level patterns see only the identity, so they ignore filter value, partition, and tag length.
- Encoding-level patterns see only encodings of their own scheme. They use `Pattern.Bits` where published, which is equivalent to field matching for translated tags, and field matching otherwise.
- Every compiler rejects empty or duplicate IDs, subscriptions without patterns, unset patterns, more than 64 patterns per subscription, and more than `MaxSubscriptions` (10,000) with the same typed errors.
- Compiled matchers are immutable and copy their input.
- `Set` holds the current matcher behind `atomic.Pointer`. `Replace` compiles first and publishes the whole matcher, so readers see the old set or the new one, never a mix; on error the current matcher stays.

Matched IDs are the hand-off to delivery: `MatchingProcessor` routes them to destinations, described in [report.md](report.md).
Subscriptions come from the configuration file ([configuration.md](configuration.md)); without one, `cmd/gosstrak` starts with an empty set.

## Matchers

`[filter] matcher` in the configuration file selects one for the process lifetime ([configuration.md](configuration.md)); every matcher honours the contract above.

| Matcher | Compiler | Role |
| --- | --- | --- |
| `index` | `CompileIndex`, `Compile` | default |
| `linear` | `CompileLinear` | reference and control group |

### Linear

The linear matcher tests every subscription in order with the reference rule.
It is simple enough to be obviously correct, which is what a reference needs, and costs about 19 ns per subscription per tag.

### Prefix index

The index narrows candidates by pattern prefix, then confirms each with the same reference rule, so it can never accept a tag linear rejects:

- Encoding patterns with a bit test go into a per-scheme byte trie over the leading run of masked bits. Anything the mask pins after that run, such as `sscc-96`'s reserved bits, is left to the confirming match.
- Patterns without a bit test whose fields are exact values followed only by stars, which includes every identity-level pattern without a range, go into a trie over component values, one per authority and key type or scheme.
- Field patterns with a range are scanned linearly.

A range in a bit-tested pattern, such as `urn:epc:pat:sgtin-96:[1-3].0614141.812345.*`, ends the leading run at the filter value, so such patterns sit near the trie root and cost what a linear scan of them would.
Lookup costs the trie depths plus the candidates found plus those range patterns, independent of how many other subscriptions there are.
Results are restored to subscription order through a rank table, and duplicates from a subscription matching through several patterns are removed, both in place, so `Match` does not allocate.

The index is the default because it was faster than linear on every workload below without allocating, the condition the slice-2 design record set.
It pays at compile time instead: `Replace` and startup rebuild it whole.

### Workload results

`internal/filter/workload_test.go` matches 1,000 tags per op against synthetic subscription sets.
No field subscription sets or tag traces exist, so the workloads are stand-ins chosen to cover the pattern shapes the matchers treat differently, not measured deployments.
They vary the subscription count (10 to 10,000), the share of subscriptions using a filter range (0, 10, or 50%), the share of tags matching (0, 1, or 25%), and the schemes (sgtin-96 only, or mixed with sscc-96 and grai-96).
Half of the subscriptions without a range are identity-level.
`TestWorkloadMatchersAgree` checks that both matchers return the same matches on these inputs.

Medians of six runs on Apple M1 Pro, darwin/arm64, Go 1.26.10, recorded 2026-10-24, at 25% matching tags; time per tag:

| Schemes | Subscriptions | Range share | Linear | Index | Change |
| --- | ---: | ---: | ---: | ---: | ---: |
| sgtin-96 | 10 | 0% | 162 ns | 68 ns | −58% |
| sgtin-96 | 100 | 0% | 1.59 µs | 100 ns | −94% |
| sgtin-96 | 1,000 | 0% | 21.6 µs | 106 ns | −99.5% |
| sgtin-96 | 10,000 | 0% | 252 µs | 123 ns | −99.95% |
| sgtin-96 | 10,000 | 10% | 264 µs | 23.0 µs | −91% |
| sgtin-96 | 10,000 | 50% | 283 µs | 117 µs | −59% |
| mixed | 10 | 50% | 156 ns | 123 ns | −21% |
| mixed | 10,000 | 0% | 229 µs | 131 ns | −99.94% |
| mixed | 10,000 | 50% | 232 µs | 94 µs | −60% |

Across all 72 workloads the index is faster in every one, by −89% in geometric mean, and neither matcher allocates.
The match share barely moves either matcher; the share of range patterns is what limits the index, as the design record expected.

Compiling the mixed workload with 10% ranges:

| Subscriptions | Linear compile | Index compile | Linear retained | Index retained |
| ---: | ---: | ---: | ---: | ---: |
| 10 | 781 ns | 4.24 µs | 1.2 KiB | 5.5 KiB |
| 1,000 | 79.9 µs | 475 µs | 118 KiB | 447 KiB |
| 10,000 | 865 µs | 4.83 ms | 1.15 MiB | 3.89 MiB |

The index compiles about 5.6 times slower and retains about 3.4 times the memory.
At the 10,000-subscription budget that is under 5 ms and 4 MiB per replacement, which subscription changes, far rarer than tags, can afford.

```sh
go test -run '^$' -bench 'Workload|Compile' -benchmem -count 6 ./internal/filter/
```

The raw output of these runs is `tagstrak-filter-workload-2026-10-24.txt` on the draft `v2.0.0` release, SHA-256 `c90c70b40d60f5c1ef824ef9bf033c8d53a20008cfff735852896f782dbf82a6`.
It was measured before merge, on `main` at `458c76e` plus the change that added the index.
The older single-pattern baseline remains as `BenchmarkLinearMatch`.

### Adaptive mode

`[filter] mode = "adaptive"` is experimental and not the default.
`CompileAdaptive` compiles every candidate, `index` and `linear` unless `[filter.adaptive] candidates` says otherwise, one at a time from the same subscriptions.
Every `Match` returns the selected candidate's result; candidates agree on every subscription set, so the selection changes cost, never results.

It starts no goroutines.
Every `sample_every`-th call (default 100) also runs one other candidate, round-robin, and times both, alternating which runs first.
Shadow results go to the matcher's own buffer and never reach the caller.
Unsampled calls read the selection atomically; only sampled calls take a lock.

| Setting | Default | Bound | Effect |
| --- | --- | --- | --- |
| `candidates` | `["index", "linear"]` | 2 to 4, no duplicates | compiled in order; the first is selected initially |
| `sample_every` | 100 | at least 10 | sampling rate |
| `warmup` | 20 | 1 to 1,000 | samples every candidate needs before any switch |
| `window` | 50 | 1 to 1,000 | recent samples per candidate a decision averages |
| `hysteresis` | 0.10 | [0, 1) | how much cheaper a challenger must be, as a fraction |
| `cooldown` | 500 | at least 0 | sampled calls after a switch before the next |
| `shadow` | `"sampled"` | | `"full"` runs every candidate on every call and needs `experimental = true` |

Memory is bounded by the candidate count times one compiled matcher under the 10,000-subscription budget, plus a fixed window per candidate.
A subscription replacement compiles a new adaptive matcher, so warmup and statistics start over.
With today's two candidates and the workloads above, the controller is expected to settle on the index; the remaining #7 slice measures what adaptivity costs and how stable its choices are.

Telemetry reports the decisions for the current snapshot: `gosstrak_filter_adaptive_{sampled,switches}_total`, `gosstrak_filter_adaptive_shadow_seconds_total`, and per `candidate` label `gosstrak_filter_adaptive_selected`, `gosstrak_filter_adaptive_samples_total`, and `gosstrak_filter_adaptive_mean_cost_seconds`.
`/healthz` and `/readyz` include the same under `filter`.

### Candidates not built

| Candidate | Reason |
| --- | --- |
| Splay tree | restructures itself on every read, which breaks immutable lock-free reads under `Set`; v1's own benchmarks put it 10 times behind its trie at 10,000 subscriptions ([benchmarks.md](benchmarks.md)) |
| v1 List | equivalent to the linear matcher |
| Hash index per exact-prefix depth | one lookup per distinct depth plus an allocation-free key hash; more machinery than the trie for the same pattern shapes |
| Aho–Corasick or other automata | patterns are fixed-position masks, not substring searches |

Splitting range patterns further, for example by the bits after the filter value, is left until a workload shows it matters.

## Design evaluation

Assumptions: matching runs after translation in the single consumer worker, and subscriptions change rarely compared with tag arrival.
A filter-owned `Subject` keeps #2's import rule; importing `tdtcore` would widen it, and an interface cannot be satisfied by `Translation` because its `Identity` field blocks an `Identity()` method.
Returning IDs instead of #2's `report.Route` keeps delivery in #9; adding routing data later is additive.
Whole-matcher replacement trades rebuild cost on every change for lock-free reads and no partial state.

## Verification

```sh
go test -race ./internal/filter/... ./internal/app/...
go vet ./...
```

Every contract test runs against the linear, index, and adaptive matchers: the `patterns.json` conformance corpus against `Translation.Match`, alternative patterns, ordering, deduplication, caller buffers, compile errors, input copying, and allocation-free matching, including the index's reordering path.
`TestIndexAgreesWithLinear` compares the matchers on 2,000 fixed-seed random subscription sets drawn from every pattern shape, and `FuzzIndexAgreesWithLinear` does the same from fuzzed input.
Adaptive decisions are tested deterministically with a fake clock and scripted candidates: warmup, hysteresis at and past the threshold, cooldown, round-robin and full shadows, shadow results kept out of the caller's buffer, and every configuration bound; a race test runs sampled and unsampled calls concurrently.
Tests also cover the zero `Set`, failed replacement, concurrent replacement under the race detector, and the import boundary.

```sh
go test -run '^$' -fuzz '^FuzzIndexAgreesWithLinear$' -fuzztime 2m ./internal/filter
```
