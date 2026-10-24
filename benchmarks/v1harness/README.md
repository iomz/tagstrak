# v1 harness

These files are v1.0.0 benchmarks with the same names and inputs as `benchmarks/v2bench`.
They are stored as `.go.txt` so the v2 module never compiles them; `tools/benchrun` places them into a temporary `v1.0.0` worktree with `go test -overlay`, leaving the tag's tree untouched.

| File | Overlaid as |
| --- | --- |
| `llrp_test.go.txt` | `llrp/zz_compare_test.go` |
| `tdt_test.go.txt` | `internal/gosstrak/tdt/zz_compare_test.go` |
| `filtering_test.go.txt` | `internal/gosstrak/filtering/zz_compare_test.go` |
