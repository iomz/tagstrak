# Legacy import

Tagstrak began as a source snapshot of three archived repositories.
Their Git histories remain available in the original repositories.

| Repository | Imported revision | Destination |
| --- | --- | --- |
| `iomz/go-llrp` | `099fc13de16983f370eff64a1149e72b718e3003` | `llrp`, `internal/binutil`, supporting commands |
| `iomz/golemu` | `ffa00370ba2903108921d9ff67f6d29f2b00b8d0` | `cmd/golemu`, `internal/golemu` |
| `iomz/gosstrak` | `ac7b683c7c9f08f86b85595f1bef113fa26372e0` | `cmd/gosstrak`, `internal/gosstrak` |

Initialization made only structural changes required by the monorepo:

- changed module and import paths to `github.com/iomz/tagstrak/v2`
- exposed LLRP as `github.com/iomz/tagstrak/v2/llrp`
- moved application-only packages beneath `internal`
- renamed the `gosstrak-fc` command directory to `gosstrak`
- combined existing dependency manifests while retaining legacy versions

Functional refactoring starts after this import baseline.
