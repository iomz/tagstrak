# tagstrak

tagstrak is RFID middleware for LLRP readers.
It connects to a reader, translates each tag into a GS1 or ISO identity, matches it against subscriptions, and delivers the matches as JSON batches over HTTP.

The repository ships two commands and one library:

- `tagstrak`: the middleware.
- `tagstrak-rig`: a companion for testing without hardware, with an LLRP reader emulator and a tag data generator.
- `github.com/iomz/tagstrak/v2/llrp`: a bounded LLRP codec for Go programs ([pkg.go.dev](https://pkg.go.dev/github.com/iomz/tagstrak/v2/llrp)).

Contents:
[Install](#install) ·
[Quick start](#quick-start) ·
[Configuration](#configuration) ·
[Patterns](#patterns) ·
[Reports](#reports) ·
[Management API](#management-api) ·
[Telemetry](#telemetry) ·
[Production](#production) ·
[Security](#security) ·
[tagstrak-rig](#tagstrak-rig) ·
[Migrating from v1](#migrating-from-v1) ·
[Development](#development)

## Install

```sh
go install github.com/iomz/tagstrak/v2/cmd/tagstrak@latest
go install github.com/iomz/tagstrak/v2/cmd/tagstrak-rig@latest
```

The module path is `github.com/iomz/tagstrak/v2`.
Go 1.23.2 or later builds it, but build releases with Go 1.26.10 or later, the toolchain `go.mod` names, which carries the current security fixes.
Both commands describe every flag and its default with `--help`.

## Quick start

From a checkout of this repository, start the emulator with the example scenario:

```sh
go run ./cmd/tagstrak-rig reader --scenario examples/rig/scenario.json
```

In another terminal, start tagstrak with the example configuration:

```sh
ERP_TOKEN=test go run ./cmd/tagstrak --config examples/tagstrak/tagstrak.toml
```

The scenario plays three SGTIN products, an SSCC pallet, and one unrelated product.
The configuration subscribes to the products and the pallet, and delivers matches to `http://127.0.0.1:9000/rfid`; any endpoint that answers a `POST` with 2xx receives them.
`http://127.0.0.1:8080/metrics` shows tags arriving and translating, and a receiver on port 9000 gets every tag except the unrelated product.

## Configuration

Settings apply in this order, later layers winning:

1. built-in defaults;
2. the configuration file named by `--config` or `TAGSTRAK_CONFIG`;
3. `TAGSTRAK_*` environment variables;
4. flags.

Every flag `--x-y` has the environment variable `TAGSTRAK_X_Y`.
The environment and flags cover scalar settings only; subscriptions, destinations, and secrets come only from the file.
Any invalid setting stops startup with the name of the setting at fault.

The file is TOML or JSON with one schema.
[`examples/tagstrak/reference.toml`](examples/tagstrak/reference.toml) lists every key at its default with what it does, and [`examples/tagstrak/tagstrak.toml`](examples/tagstrak/tagstrak.toml) is a small working example.
YAML is not accepted, because YAML 1.1 can turn a company prefix such as `0614141` into a number.

### Secrets

```toml
[destinations.headers.Authorization]
env = "ERP_TOKEN"        # or: file = "/run/secrets/erp-token"
prefix = "Bearer "       # optional, prepended literally, not secret
```

- A source is exactly one of `env` or `file`; inline values are rejected, so the configuration file can be committed.
- An `env` source must be set and non-empty.
- A `file` source must be a regular file of at most 4 KiB, with no group or other permissions; one trailing newline is trimmed.
- Secrets are read once at startup, so rotating one means a restart.
- Secrets print as `[redacted]` in logs and errors, and no flag accepts one.

### Subscription source and reload

`[subscription_store] source` chooses where subscriptions live:

- `"file"`, the default: the file's `[[subscriptions]]`. `SIGHUP` re-reads them and replaces the set atomically; an invalid file is rejected and the current set stays.
- `"api"`: a state file at `state_file`, replaced through the [management API](#management-api) and written atomically with mode 0600. The configuration file must list no subscriptions, and `[control]` is required.

Destinations, secrets, the matcher, and every other setting change only on restart.

## Patterns

Subscriptions match tags with GS1 EPC patterns:

- `urn:epc:idpat:sgtin:0614141.812345.*` matches an identity, whatever its filter value or tag length.
- `urn:epc:pat:sgtin-96:3.0614141.812345.*` matches one encoding, including its filter value.

A pattern names every field of its scheme.
Each field is a value or `*`, and a `*` may be followed only by more `*`.
Encoding-level patterns also accept a numeric range such as `[1-500]` in any field after the company prefix.
A pattern that does not compile stops startup with the field that is wrong.
A subscription matches when any of its patterns matches, and may hold up to 64 patterns; a process holds up to 10,000 subscriptions.

tagstrak translates SGTIN-96, SSCC-96, GIAI-96, and GRAI-96 from GS1 TDS 2.2, and ISO/IEC 17360 transport units (AFI `0xA2`, `0xA7`).
ISO 17363 containers (AFI `0xA9`, `0xAA`) are recognised and refused, because how a container code is carried on a Gen2 tag is not established.
Other tags are counted as untranslatable and dropped.
ISO publishes no pattern syntax, so ISO identities cannot be subscribed to with a pattern string yet.

## Reports

Each destination has its own queue and worker, so a slow destination never delays another or the reader.
A destination receives one `POST` per batch, `Content-Type: application/json`, with the batch ID as `Idempotency-Key`:

```json
{
  "schema": "tagstrak.report.v1",
  "batch": "3f2a…:0000000001",
  "events": [
    {
      "id": "9c41…:1",
      "source": "dock-1",
      "observedAt": "2026-10-24T09:00:00Z",
      "subscriptions": ["product"],
      "scheme": "sgtin-96",
      "epc": "3074257bf7194e4000001a85",
      "identity": {
        "authority": "gs1",
        "keyType": "sgtin",
        "components": {"gs1companyprefix": "0614141", "itemref": "812345", "serial": "6789"}
      },
      "pureIdentityURI": "urn:epc:id:sgtin:0614141.812345.6789"
    }
  ]
}
```

- There is one event per observation and destination, listing that destination's matched subscriptions; events for one observation share an `id` across destinations.
- `pureIdentityURI` is omitted when the identity has no standard URI form, as for ISO identities.
- Fields may be added under `tagstrak.report.v1`; changing a field's meaning needs a new schema name.

Delivery is at-least-once while the process runs:

- **Duplicates:** a retry after a timeout can repeat a batch the destination already committed, so receivers deduplicate on the event `id`.
- **Ordering:** first in, first out per destination; none across destinations.
- **Retries:** 2xx is success; 408, 425, 429, 5xx, and transport errors are retried with backoff, honouring `Retry-After`; anything else is a final failure. With the defaults, a destination may be down for about 23 seconds without losing a batch.
- **Loss:** only on a full queue, a final failure, the shutdown deadline, or a crash. The first three are counted per destination, and undelivered reports at shutdown make tagstrak exit nonzero. A crash loses whatever is queued in memory without counting it.

Redirects are never followed, TLS is always verified against the system roots, and destination URLs never appear in logs, errors, or metrics.
Put credentials in header secrets, never in the URL.

## Management API

The optional management API reads status and replaces subscriptions.
It is off unless `[control]` names one listener:

- **Unix socket** (`--control-socket` or `[control] socket`): an absolute path of at most 103 bytes on macOS or 107 on Linux, in a directory with no group or other permissions. It trusts the same local user and needs no token.
- **Loopback TCP** (`--control-address` or `[control] address`): loopback only, always with a bearer token of at least 32 bytes from `[control] token`. Other addresses are refused; reach it remotely through an SSH tunnel or a proxy on the same host.

| Route | Behaviour |
| --- | --- |
| `GET /v1/status` | The telemetry status document |
| `GET /v1/subscriptions` | `{"source", "revision", "subscriptions"}`; the revision is also the `ETag` |
| `PUT /v1/subscriptions` | Replace the whole set; `"api"` source only |

`PUT` takes `{"subscriptions": [...]}` in the configuration file's subscription schema and honours `If-Match: "<revision>"`.
`[]` removes every subscription; a missing or `null` field is rejected.
The new set is validated, written to the state file, and only then published, so a failed write changes nothing.
The API cannot add destinations or change URLs or headers.

```sh
curl --unix-socket /run/tagstrak/control.sock http://localhost/v1/subscriptions

curl -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -H 'If-Match: "3"' \
  -X PUT --data '{"subscriptions":[{"id":"product","patterns":["urn:epc:idpat:sgtin:0614141.812345.*"],"destinations":["erp"]}]}' \
  http://127.0.0.1:9090/v1/subscriptions
```

| Status | Code | Meaning |
| --- | --- | --- |
| 200 | | Replaced; the body and `ETag` carry the new revision |
| 400 | `invalid_json`, `invalid_subscriptions`, `invalid_precondition` | Malformed body, invalid subscription (with `field`), or malformed `If-Match` |
| 401 | `unauthenticated` | Missing or wrong token |
| 409 | `managed_by_file` | The configuration file owns subscriptions; edit it and send `SIGHUP` |
| 412 | `revision_mismatch` | `If-Match` is not the current revision |
| 413 | `body_too_large`, `state_too_large` | Body or resulting state file over 1 MiB |
| 415 | `unsupported_media_type` | Not `application/json` |
| 500 | `state_not_saved` | The state file could not be written |

Every mutation, reload, and authentication failure is logged at info level with `event=control`, its `action`, `outcome`, `actor`, and `request_id`, never with payloads or tokens.

## Telemetry

The telemetry listener (`--telemetry`, default `127.0.0.1:8080`) is read-only and unauthenticated:

- `GET /healthz`: process liveness, 503 before running or after shutdown.
- `GET /readyz`: 200 while the reader session is serving, 503 while connecting, reconnecting, failed, or draining.
- `GET /metrics`: Prometheus text format.

Report families appear once destinations are configured, and `tagstrak_filter_adaptive_*` families only in adaptive filter mode.

| Metric | Type | Label | Meaning |
| --- | --- | --- | --- |
| `tagstrak_ready` | gauge |  | 1 when the reader session is serving, else 0. |
| `tagstrak_reader_attempts_total` | counter |  | Reader connection attempts. |
| `tagstrak_reader_reconnects_total` | counter |  | Reader reconnections after a lost session. |
| `tagstrak_reader_frames_total` | counter |  | LLRP frames read from the reader. |
| `tagstrak_reader_reports_total` | counter |  | RO_ACCESS_REPORT messages read from the reader. |
| `tagstrak_reader_keepalives_total` | counter |  | Keepalives acknowledged. |
| `tagstrak_reader_failures_total` | counter |  | Reader connection or session failures. |
| `tagstrak_reader_retry_delay_seconds` | gauge |  | Current reconnect delay, 0 while connected. |
| `tagstrak_queue_overflows_total` | counter |  | Observations refused by a full queue; any overflow fails the run. |
| `tagstrak_queue_depth` | gauge |  | Observations waiting for processing. |
| `tagstrak_queue_capacity` | gauge |  | Observation queue capacity. |
| `tagstrak_observations_received_total` | counter |  | Tag observations decoded from reader reports. |
| `tagstrak_observations_processed_total` | counter |  | Observations translated and matched. |
| `tagstrak_observations_abandoned_total` | counter |  | Observations not processed: still queued at the shutdown deadline, or failed in processing. |
| `tagstrak_observations_translated_total` | counter |  | Observations translated to an identity. |
| `tagstrak_observations_untranslatable_total` | counter |  | Observations in schemes tagstrak does not support. |
| `tagstrak_report_enqueued_total` | counter | `destination` | Report events accepted into a destination queue. |
| `tagstrak_report_delivered_total` | counter | `destination` | Report events delivered. |
| `tagstrak_report_dropped_total` | counter | `destination` | Report events dropped by a full destination queue. |
| `tagstrak_report_failed_total` | counter | `destination` | Report events in batches that finally failed. |
| `tagstrak_report_undelivered_total` | counter | `destination` | Report events still queued at the shutdown deadline. |
| `tagstrak_report_retries_total` | counter | `destination` | Delivery retries. |
| `tagstrak_report_queue_depth` | gauge | `destination` | Report events waiting for delivery. |
| `tagstrak_report_queue_capacity` | gauge | `destination` | Destination queue capacity. |
| `tagstrak_filter_adaptive_sampled_total` | counter |  | Adaptive matcher calls sampled for measurement. |
| `tagstrak_filter_adaptive_shadows_skipped_total` | counter |  | Shadow evaluations skipped by the overhead budget. |
| `tagstrak_filter_adaptive_switches_total` | counter |  | Adaptive matcher switches. |
| `tagstrak_filter_adaptive_shadow_seconds_total` | counter |  | Time spent in shadow evaluations. |
| `tagstrak_filter_adaptive_selected` | gauge | `candidate` | 1 for the candidate matcher currently selected. |
| `tagstrak_filter_adaptive_samples_total` | counter | `candidate` | Measurements per candidate matcher. |
| `tagstrak_filter_adaptive_mean_cost_seconds` | gauge | `candidate` | Mean measured cost per candidate matcher. |

## Production

tagstrak fails fast rather than degrading silently.
It exits nonzero when its reconnect budget is spent, when the observation queue overflows, when processing fails, or when reports are still undelivered at the shutdown deadline.
SIGINT and SIGTERM shut it down cleanly.
Run one tagstrak per reader under a supervisor that restarts it, for example systemd:

```ini
[Unit]
Description=tagstrak RFID middleware
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/tagstrak --config /etc/tagstrak/tagstrak.toml
ExecReload=/bin/kill -HUP $MAINPID
EnvironmentFile=/etc/tagstrak/secrets.env
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
User=tagstrak
StateDirectory=tagstrak
RuntimeDirectory=tagstrak
RuntimeDirectoryMode=0700
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

Keep `TimeoutStopSec` above `[runtime] shutdown_timeout`, so queued reports can drain before the supervisor kills the process.
`RuntimeDirectory` suits a control socket and `StateDirectory` an `"api"` state file.
Size `[runtime] queue_capacity` for the largest burst the reader can deliver faster than tagstrak processes it, since overflow ends the run.

Alert on:

- `tagstrak_ready` at 0 for longer than a reconnect cycle;
- `tagstrak_reader_failures_total` or `tagstrak_reader_reconnects_total` rising;
- `tagstrak_queue_depth` approaching `tagstrak_queue_capacity`;
- `tagstrak_report_dropped_total`, `tagstrak_report_failed_total`, or `tagstrak_report_undelivered_total` increasing for any destination;
- `tagstrak_observations_untranslatable_total` rising, which usually means tags in an unsupported scheme.

## Security

| Boundary | Trust | Controls |
| --- | --- | --- |
| LLRP reader network | Untrusted bytes | Frame, parameter, and count limits; per-frame deadlines; bounded queue; reconnect budget |
| Management API | Same local user, or token holder on loopback | Off by default; loopback only; constant-time token check; size, connection, and time limits; audit log |
| Telemetry | Read-only, unauthenticated | Loopback by default; no mutation routes; connection cap and deadlines |
| Report destinations | Outbound, partially trusted | Verified TLS; no redirects; URLs never logged; bounded retries |
| Configuration, state, and secret files | Local operator | Strict decoding; size limits; no inline secrets; private secret files; atomic private state writes |

Known limits:

- Authentication failures are not rate-limited beyond the connection cap and deadlines.
- The Unix socket trusts every process running as the same user.
- Remote management depends on an external tunnel or proxy for TLS.
- Telemetry shows destination names and counters to anyone who can reach it.
- Reports are held in memory and lost on a crash.

## tagstrak-rig

tagstrak-rig emulates an LLRP reader: it speaks real LLRP to tagstrak and reports tags from a scenario, but does not model radio propagation.
It also generates tag data for scenarios and patterns for subscriptions.

### Scenarios and inventories

`reader --scenario` plays a scenario: a list of inventory cycles, one report per cycle.

```sh
go run ./cmd/tagstrak-rig reader --scenario examples/rig/scenario.json --report-interval 1s
```

```json
{
  "version": 1,
  "seed": 42,
  "repeat": true,
  "shuffle": true,
  "cycles": [
    ["3074257bf7194e4000000001", "3074257bf7194e4000000002", "3174257bf4499602d2000000"],
    ["3074257bf7194e4000000002", "3074257bf7194e4000000003", "3074257bf76ee84000000007"],
    []
  ]
}
```

`version`, `seed`, and `cycles` are required; `repeat` and `shuffle` default to false.
Each tag is a lowercase hexadecimal EPC of 2 to 62 bytes, whole 16-bit words; a tag may appear once per cycle.
A scenario holds at most 1,024 cycles and 100,000 tags in all.

`reader --inventory` repeats one inventory instead, shuffled with `--shuffle` and `--seed` if asked:

```sh
go run ./cmd/tagstrak-rig reader --inventory examples/rig/inventory.json
```

```json
{"version": 1, "tags": ["3074257bf7194e4000000001", "3174257bf4499602d2000000"]}
```

Runs are reproducible: for the same scenario and seed, every connection receives the same report bytes, and shuffling draws its ordering from the seed.
Each connection starts at the first cycle; a reconnect starts over.

### Generate tag data

`uii` prints one tag as `PC,EPC` in hex, ready for a scenario:

```sh
go run ./cmd/tagstrak-rig uii epc --scheme sgtin-96 --company-prefix 0614141 --item-ref 812345 --serial 6789
# 3000,3074257bf7194e4000001a85
```

`epc` supports `sgtin-96`, `sscc-96`, `giai-96`, and `grai-96`; fields left out are generated at random.
`iso17360` generates an ISO/IEC 17360 transport unit, and `iso17363` checks an ISO 6346 container code but refuses to write it.
`--pattern` prints a subscription pattern and the bit prefix it pins instead; the first field left out and every field after it become `*`:

```sh
go run ./cmd/tagstrak-rig uii epc --scheme sscc-96 --company-prefix 0614141 --pattern
# urn:epc:pat:sscc-96:3.0614141.*,00110001011101000010010101111011111101
```

### Replace the scenario while running

With `--control-socket`, tagstrak-rig serves `GET /status` and `PUT /scenario` on a Unix socket in a private directory:

```sh
dir=$(mktemp -d /tmp/rig.XXXXXX)
go run ./cmd/tagstrak-rig reader --scenario examples/rig/scenario.json --control-socket "$dir/control.sock"

curl --unix-socket "$dir/control.sock" -X PUT -H 'Content-Type: application/json' \
  --data-binary @examples/rig/scenario.json http://localhost/scenario
```

A replacement takes effect at each connection's next cycle boundary and restarts the scenario from its first cycle; the input file is never changed.

### Container image

```sh
docker build -f build/tagstrak-rig/Dockerfile -t tagstrak-rig .
docker run --rm -p 5084:5084 tagstrak-rig reader --scenario /examples/scenario.json --listen 0.0.0.0:5084
```

The image runs as an unprivileged user and carries the example scenario and inventory under `/examples`.

## Migrating from v1

v2 is a rewrite with the same purpose, not a drop-in upgrade.
v1 flags and behaviour are not compatibility contracts, and v1 remains available at the `v1.0.0` tag.

| v1 | v2 |
| --- | --- |
| `gosstrak start --ip ADDR` | `tagstrak --reader HOST:PORT`, or `[reader] address` |
| `golemu` | `tagstrak-rig` |
| `uiigen` | `tagstrak-rig uii` |
| `tagjson`, `random*`, `sixenc2bin` | removed |
| `--ecspecfile ecspec.csv` with `reportURI,urn:epc:pat:…` rows | `[[subscriptions]]` with `id`, `patterns`, and `destinations` |
| report URIs in the ECSpec, never delivered to | `[[destinations]]` that receive JSON batches over HTTP |
| `--managementAddr` pseudo-ALE endpoint | the optional management API |
| `--enableStat` and InfluxDB flags | `/metrics` in Prometheus text format |
| filtering engines swapped at run time | the prefix index; adaptive mode is opt-in and experimental |
| `/var/tmp/gosstrak-fc-cache` | no on-disk cache; with `source = "api"`, one state file |
| `cmd/ale-ec` | removed |

To convert an ECSpec, make each row a subscription and give each report URI you want delivered a destination:

```text
http://erp.example/rfid,urn:epc:pat:sgtin-96:3.0614141.812345
```

```toml
[[destinations]]
name = "erp"
url = "https://erp.example/rfid"

[[subscriptions]]
id = "product-812345"
patterns = ["urn:epc:pat:sgtin-96:3.0614141.812345.*"]
destinations = ["erp"]
```

v1's shorter prefix patterns gain `.*` for the fields they omit.
The `llrp` package dropped v1's untyped builders; see its [package documentation](https://pkg.go.dev/github.com/iomz/tagstrak/v2/llrp).

## Development

See [AGENTS.md](AGENTS.md) for the checks every change passes, the code layout, and the conventions.
Design decisions are recorded on the GitHub issues and pull requests that made them.

## License

MIT; see [LICENSE](LICENSE).
The vendored GS1 Tag Data Translation files are Apache-2.0; see [LICENSES/gs1-tdt.txt](LICENSES/gs1-tdt.txt).
