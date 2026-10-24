# tagstrak

Modern Go stack for LLRP-based RFID, combining reusable protocol tooling, gosstrak middleware, and golemu reader emulation.

## Commands

- `gosstrak`: RFID event filtering and collection middleware
- `golemu`: LLRP reader emulator

## Packages

- `llrp`: reusable public LLRP package
- `internal/emulator`: deterministic scenarios and LLRP emulation
- `internal/app/golemu`: golemu composition and local control
- `internal/identity`, `internal/tdt`: canonical identities and tag data translation
- `internal/filter`: subscription matching over translated tags
- `internal/report`: bounded report delivery and destination adapters
- `internal/config`: versioned configuration file and redacted secret sources
- `internal/control`: hardened management transport shared by both commands
- `internal/gosstrak`: gosstrak application packages

## Legacy import

Initial source was moved from the archived `go-llrp`, `golemu`, and `gosstrak` repositories without intentional behavior changes.
See [docs/legacy-import.md](docs/legacy-import.md) for source revisions and path mapping.

## Reader runtime

The v2 gosstrak ingestion runtime provides bounded reader sessions, finite reconnects, and health/readiness/metrics endpoints.
See [docs/runtime.md](docs/runtime.md) for configuration, lifecycle, and the current milestone scope.

## Reader emulation

Golemu runs repeating inventories and seeded scenarios through one bounded runtime, with optional private Unix-socket control.
See [docs/golemu.md](docs/golemu.md) for runnable examples and cycle semantics.

## Tag data translation

Gosstrak translates tag data into canonical GS1 EPC and ISO/IEC 17360 identities from pinned GS1 TDT artefacts and cited ISO evidence, and compiles GS1 patterns for filtering.
See [docs/tdt.md](docs/tdt.md) for supported schemes, artefact maintenance, golden vectors, and the benchmark baseline.

## Subscription matching

Translated tags are matched against an immutable, atomically replaced subscription set.
See [docs/filter.md](docs/filter.md) for the matcher contract, conformance, and the single-mode baseline.

## Report delivery

Matches are delivered as JSON batches over HTTP through bounded per-destination queues with bounded retries.
See [docs/report.md](docs/report.md) for the delivery contract, bounds, and wire schema.

## Management and security

An optional management API on a private Unix socket or authenticated loopback TCP reads status and replaces subscriptions.
See [docs/control.md](docs/control.md) and the threat model in [docs/security.md](docs/security.md).

## Configuration

Gosstrak reads an optional TOML or JSON configuration file for its settings, subscriptions, and destinations, with secrets from environment or file sources only.
See [docs/configuration.md](docs/configuration.md) and [examples/gosstrak/gosstrak.toml](examples/gosstrak/gosstrak.toml).
