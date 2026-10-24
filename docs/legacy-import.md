# Legacy import

Tagstrak began as a source snapshot of three archived repositories.
Their Git histories remain available in the original repositories.

| Repository | Imported revision | Destination |
| --- | --- | --- |
| `iomz/go-llrp` | `099fc13de16983f370eff64a1149e72b718e3003` | `llrp`, `internal/binutil`, supporting commands |
| `iomz/golemu` | `ffa00370ba2903108921d9ff67f6d29f2b00b8d0` | `cmd/golemu`, `internal/golemu` |
| `iomz/gosstrak` | `ac7b683c7c9f08f86b85595f1bef113fa26372e0` | `cmd/gosstrak`, `internal/gosstrak` |

Initialization made only structural changes required by the monorepo:

- changed module and import paths to `github.com/iomz/tagstrak`, which is the module path at the `v1.0.0` tag; the `/v2` suffix followed in `68442ba` with the v2 work
- exposed LLRP as `github.com/iomz/tagstrak/llrp`, now `github.com/iomz/tagstrak/v2/llrp`
- moved application-only packages beneath `internal`
- renamed the `gosstrak-fc` command directory to `gosstrak`
- combined existing dependency manifests while retaining legacy versions

Functional refactoring starts after this import baseline.
The `v1.0.0` tag marks it, and the v1 benchmark baseline (#11) is measured from that tag.
The imported legacy packages that v2 superseded (`internal/gosstrak`, `cmd/ale-ec`, and the `deploy` compose file) and the dependencies only they used were removed in #11.
