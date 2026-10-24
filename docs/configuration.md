# Configuration

Issue #10 slice 10a adds file and environment configuration to gosstrak in the v2 milestone tracked by #1.
The [#10 design record](https://github.com/iomz/tagstrak/issues/10#issuecomment-6378251881) is the decision source; runtime management through a control API is slice 10b.

## Layers

Settings apply in this order, later layers winning (#2):

1. built-in defaults;
2. the configuration file named by `--config` or `GOSSTRAK_CONFIG`;
3. `GOSSTRAK_*` environment variables;
4. flags.

Every flag `--x-y` has the environment variable `GOSSTRAK_X_Y`, derived from the flag set, so `--read-timeout` is `GOSSTRAK_READ_TIMEOUT`.
The environment and flags cover scalar settings only.
Subscriptions, destinations, and header secrets come only from the file.
The command builds one validated configuration before opening any listener or connection; any error stops startup.

```sh
ERP_TOKEN=... go run ./cmd/gosstrak --config examples/gosstrak/gosstrak.toml
```

## File

The file is TOML (`.toml`) or JSON (`.json`) with one schema; see [examples/gosstrak/gosstrak.toml](../examples/gosstrak/gosstrak.toml).
Decoding is strict: unknown keys, trailing JSON data, a `version` other than `1`, or a file over 1 MiB is an error.
Durations are Go duration strings such as `"5s"`.
A setting the file omits keeps its default; an explicit zero, such as `max_retries = 0`, is kept.

YAML is not accepted.
YAML 1.1 parsers resolve unquoted scalars implicitly, which can turn a GS1 company prefix with a leading zero, such as `0614141`, into a number, and `no` or `on` into booleans.

| Section | Keys |
| --- | --- |
| `[reader]` | `address`, `source`, `connect_timeout`, `handshake_timeout`, `read_timeout`, `write_timeout`, `retry_min`, `retry_max`, `max_retries`, `max_frame_bytes`, `max_parameter_bytes`, `max_parameters` |
| `[telemetry]` | `address` |
| `[runtime]` | `queue_capacity`, `consume_timeout`, `shutdown_timeout` |
| `[report]` | `queue_capacity`, `batch_events`, `batch_bytes`, `flush_interval`, `request_timeout`, `max_attempts`, `backoff_base`, `backoff_max`, `retry_budget` |
| `[[destinations]]` | `name`, `url`, `headers.<Header-Name>` secret sources |
| `[[subscriptions]]` | `id`, `patterns`, optional `destinations` |

Values are validated by the component that owns them, with the same rules and bounds as flags: the reader and runtime settings by the app configuration, the report settings by `report.Config` ([report.md](report.md)), and subscriptions by `filter.Compile` ([filter.md](filter.md)).
Patterns are compiled by `tdt.CompilePattern` at startup, and each subscription may hold at most 64.
Errors name the field, such as `subscriptions[1].patterns[0]`.

A subscription's `destinations` are its routes; one without destinations is matched and logged at debug level only.
Routes must name configured destinations.
A destination URL must not carry user information; credentials go in header secrets.

## Secrets

```toml
[destinations.headers.Authorization]
env = "ERP_TOKEN"        # or: file = "/run/secrets/erp-token"
prefix = "Bearer "       # optional, prepended literally, not secret
```

- A source is exactly one of `env` or `file`. Inline values are rejected, so the file can be committed or shared.
- An `env` source must be set and non-empty.
- A `file` source must be a regular file of at most 4 KiB with no group or other permissions; one trailing newline is trimmed.
- Secrets are resolved once at startup; rotation means a restart. A missing or invalid source stops startup.
- `config.Secret` prints `[redacted]` through `fmt`, `slog`, and JSON. Only the HTTP adapter's header function reads the value.
- No flag accepts secret material.

## Design evaluation

Assumptions: the operator owns the file through configuration management, and secrets are injected by the platform as environment variables or files.
`internal/config` owns the versioned file schema and strict decoding; `internal/app/gosstrak` maps it onto its own types, so the operator-facing schema and internal configuration change independently, at the cost of declaring settings twice.
`TestFixtureSetsEverySetting` and `TestApplyFileMapsEverySetting` fail when a schema field is added without being mapped, and the TOML and JSON fixtures must decode identically.
TOML adds one dependency, `github.com/BurntSushi/toml`, which itself depends only on the standard library and is confined to `internal/config`.

## Verification

```sh
go test -race ./internal/config/... ./internal/app/... ./cmd/gosstrak/...
go vet ./...
```

Tests cover equivalent TOML and JSON decoding, unknown keys, trailing data, versions, formats, durations, size limits, inline and conflicting secret sources, redaction through every formatter, fail-closed environment and file sources, layering of defaults, file, environment and flags, invalid environment values, mapping of every setting, field-named pipeline errors, delivery of a header secret to an HTTP destination, and the shipped example.
