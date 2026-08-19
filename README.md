# tagstrak

Modern Go stack for LLRP-based RFID, combining reusable protocol tooling, gosstrak middleware, and golemu reader emulation.

## Commands

- `gosstrak`: RFID event filtering and collection middleware
- `golemu`: LLRP reader emulator

## Packages

- `llrp`: reusable public LLRP package
- `internal/golemu`: golemu application packages
- `internal/gosstrak`: gosstrak application packages

## Legacy import

Initial source was moved from the archived `go-llrp`, `golemu`, and `gosstrak` repositories without intentional behavior changes.
See [docs/legacy-import.md](docs/legacy-import.md) for source revisions and path mapping.
