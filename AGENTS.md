# Agent instructions

These instructions are for contributors, human or agent.
`README.md` is for users; design decisions live on the GitHub issues and pull requests that made them.

When a repository’s documentation and its existing code disagree, follow the conventions used in the existing code, and in your final reply point out each inconsistency you found between the documentation and the code.

## Checks

Every pull request runs these on Go 1.26.10 and 1.27.3, and they must pass locally before you push:

```sh
test -z "$(gofmt -l .)"
go mod tidy -diff
go run ./tools/tdtsync && go generate ./internal/tdt/... && git diff --exit-code
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 ./...   # Go 1.26.10 only
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
go test -race ./...
docker build -f build/tagstrak-rig/Dockerfile -t tagstrak-rig .
```

Analysis tools are pinned and run with `go run`, so they stay out of `go.mod`.
Bump a pin deliberately, after the new release has been out for a while.

## Layout

| Path | Owns |
| --- | --- |
| `llrp` | The public LLRP codec; encodes and decodes only, never dials, retries, or logs |
| `cmd/tagstrak`, `internal/app/tagstrak` | The middleware command and its composition: flags, configuration, pipeline, telemetry, management routes |
| `cmd/tagstrak-rig`, `internal/app/rig` | The emulator command, its `uii` generator, and its local control |
| `internal/reader` | One reader session: handshake, deadlines, observation decoding, reconnect budget |
| `internal/inventory` | Immutable observations and inventory files |
| `internal/identity`, `internal/tdt/...` | Canonical identities, GS1 and ISO translation, pattern compilation |
| `internal/filter` | Subscription matchers |
| `internal/report`, `internal/report/httpreport` | Bounded per-destination delivery and the HTTP adapter |
| `internal/config` | The configuration file schema, secrets, and the subscription state file |
| `internal/control` | The management transport shared by both commands |
| `internal/emulator` | Scenarios and LLRP serving for tagstrak-rig |
| `internal/e2e` | Both commands in-process over real sockets |
| `tools`, `benchmarks`, `results` | Artefact sync, test vectors, and the v1 comparison benchmark |

`internal/identity` and `internal/tdt/...` import nothing else from this module; `internal/tdt/boundary_test.go` enforces it.

## Invariants

- `go.mod` declares `go 1.23.2`, so newer language and library features such as `testing.T.Context` and `sync.WaitGroup.Go` are unavailable.
- Every budget is bounded: queues, frames, bodies, connections, retries, and timeouts have explicit limits, and overflow is a counted, visible failure.
- Constructors validate without I/O or goroutines, and every `Run` joins the goroutines it starts. Each package that starts goroutines has a `TestMain` with `goleak.VerifyTestMain`.
- Secrets travel only as `config.Secret` and are never logged, returned, or put in URLs.
- New third-party dependencies need a reason recorded on the issue; today there are only `BurntSushi/toml` and, in tests, `goleak`.
- Parsers of untrusted input have fuzz targets; commit any input that finds a bug under the package's `testdata/fuzz/`. CI runs only the seeds, so fuzz a changed parser by hand, for example `go test -run '^$' -fuzz '^FuzzDecodeTOML$' -fuzztime 2m ./internal/config`.
- These tests keep the user documentation honest; update the documentation rather than the test:
  - `TestMetricsAreDocumented`: the `README.md` metrics table matches `/metrics`.
  - `TestReferenceSetsEveryDefault`: `examples/tagstrak/reference.toml` names every setting at its default.
  - `TestShippedExamplesWorkTogether`: the tagstrak and tagstrak-rig examples work as a pair.

## Maintenance

GS1 Tag Data Translation artefacts are vendored and pinned:

```sh
go run ./tools/tdtsync                          # verify vendored files against the lock (offline)
go run ./tools/tdtsync -update -commit <sha>    # deliberate upgrade; review the regenerated Go in the same commit
go generate ./internal/tdt/...                  # offline and deterministic
```

The v1 comparison benchmark runs on one otherwise idle machine and never modifies the `v1.0.0` tag:

```sh
go run ./tools/benchrun -out results/<date>-<purpose> -count 10 -e2e-runs 10
```

## Workflow

- Record a design on its issue before implementing it: the decision, alternatives, tradeoffs, and acceptance criteria.
- Branch from `main` as `feat/`, `fix/`, `refactor/`, `docs/`, or `chore/`; pull requests are rebase-merged.
- Reference an issue with `Refs #N` until every acceptance criterion is met, then `Closes #N`.
- Commit messages follow Conventional Commits with a lowercase type and scope and an imperative subject; mark a breaking change with `!`.
- Put user-facing documentation in `README.md` and contributor documentation here; do not add other Markdown pages, except a short README beside a tool's own files, such as `benchmarks/v1harness/README.md`.

## Commit attribution

A commit materially authored by AI tools ends with one `Co-authored-by` trailer per tool that materially authored it, each naming that tool in the format the tool specifies, and no other trailers.
Examples from this repository's history:

```text
Co-authored-by: Codex <noreply@openai.com>
Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
```

Do not add session, conversation, or link trailers, or any other generated trailer.
Do not add a co-author trailer to commits the maintainer wrote, even when a tool committed them; ask about attribution for uncommitted work whose authorship is unclear.
