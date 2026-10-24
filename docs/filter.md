# Subscription matching

Issue #7 rebuilds filtering in the v2 milestone tracked by #1.
This page covers slice 1 from the [#7 design record](https://github.com/iomz/tagstrak/issues/7): the matcher contract, the single-mode baseline matcher, and atomic replacement.
Candidate evaluation, adaptive mode, and workload benchmarks remain open on #7.

## Packages and scope

| Package | Imports | Owns |
| --- | --- | --- |
| `internal/filter` | `identity` | `Subject`, `Subscription`, `Matcher`, `Compile`, `Set` |
| `internal/app/gosstrak` | `filter`, `tdt`, `report` | `SubjectOf` adapter, `MatchingProcessor`, routing to delivery |

`internal/filter` never parses pattern text and never imports the translator; `TestFilterDependsOnlyOnIdentity` enforces that.
Patterns are compiled by `tdt.CompilePattern` ([tdt.md](tdt.md)), so malformed text is rejected before filtering sees it.
`SubjectOf` builds a `filter.Subject` from a translation's raw fields and bits without copying, mirroring how `TranslationInput` adapts observations on the way in.

## Contract

```go
m, err := filter.Compile([]filter.Subscription{
	{ID: "product", Patterns: []identity.Pattern{p1, p2}},
})
ids := m.Match(subject, buf[:0])
```

- A subscription matches when any of its patterns matches. Exclusion patterns are deferred until a consumer needs them.
- `Match` appends IDs to a caller-owned buffer in subscription order, each at most once, and does not allocate.
- Identity-level patterns see only the identity, so they ignore filter value, partition, and tag length.
- Encoding-level patterns see only encodings of their own scheme. They use `Pattern.Bits` where published, which is equivalent to field matching for translated tags, and field matching otherwise.
- `Compile` rejects empty or duplicate IDs, subscriptions without patterns, unset patterns, and more than `MaxSubscriptions` (10,000) with typed errors.
- Compiled matchers are immutable and copy their input.
- `Set` holds the current matcher behind `atomic.Pointer`. `Replace` compiles first and publishes the whole matcher, so readers see the old set or the new one, never a mix; on error the current matcher stays.

Matched IDs are the hand-off to delivery: `MatchingProcessor` routes them to destinations, described in [report.md](report.md).
Subscriptions come from the configuration file ([configuration.md](configuration.md)); without one, `cmd/gosstrak` starts with an empty set.

## Single-mode baseline

The linear matcher tests every subscription in order.
It is the production default and the control group that later #7 candidates (trie, splay, adaptive) must beat with evidence while passing the same conformance tests.
Medians of six runs on Apple M1 Pro, darwin/arm64, Go 1.26.10, recorded 2026-10-23, with one matching subscription placed last:

| Subscriptions | ns/op | B/op | allocs/op |
| ---: | ---: | ---: | ---: |
| 10 | 202 | 0 | 0 |
| 100 | 1,946 | 0 | 0 |
| 1,000 | 18,686 | 0 | 0 |
| 10,000 | 189,389 | 0 | 0 |

Cost is about 19 ns per subscription per tag.
That is acceptable as a baseline and insufficient at v1's 10,000-subscription benchmark sizes, which is the gap later candidates must close.

```sh
go test -run '^$' -bench . -benchmem -count 6 ./internal/filter/
```

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

Tests cover the `patterns.json` conformance corpus against `Translation.Match`, alternative patterns, ordering, deduplication, caller buffers, compile errors, input copying, the zero `Set`, failed replacement, concurrent replacement under the race detector, allocation-free matching, and the import boundary.
