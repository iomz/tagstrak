# tagstrak

Modern Go stack for LLRP-based RFID, combining reusable protocol tooling, gosstrak middleware, and golemu reader emulation.

## Commands

- `gosstrak`: RFID event filtering and collection middleware
- `golemu`: LLRP reader emulator

## Packages

- `llrp`: reusable public LLRP package
- `internal/emulator`: deterministic scenarios and LLRP emulation
- `internal/app/golemu`: golemu composition and local control
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
