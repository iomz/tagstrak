# Deployment

This page covers running tagstrak in production and moving from v1.
Settings are described in [configuration.md](configuration.md), the runtime in [runtime.md](runtime.md), and the management API in [control.md](control.md).

## Install

```sh
go install github.com/iomz/tagstrak/v2/cmd/tagstrak@v2.0.0
```

The module path is `github.com/iomz/tagstrak/v2`; v1 was `github.com/iomz/tagstrak`.
`go.mod` sets the Go 1.26.10 toolchain as the floor; CI also tests Go 1.27.3.

## Run under a supervisor

tagstrak fails fast rather than degrading silently.
It exits nonzero when its reconnect budget is spent, when the observation queue overflows, when processing fails, or when reports are still undelivered at the shutdown deadline.
SIGINT and SIGTERM request shutdown and exit zero unless shutdown itself fails, for example with reports still undelivered.
Run it under a supervisor that restarts it: each run's reconnect budget is small by design, and restarts continue at the supervisor's pace.
A systemd unit, for example:

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

Keep `TimeoutStopSec` above `[runtime] shutdown_timeout` (default 10s), so tagstrak can drain queued reports before the supervisor kills it.
`ExecReload` sends SIGHUP, which re-reads subscriptions when they come from the file; everything else, including destinations, secrets, and the matcher, changes only on restart.

## Settings to decide

| Setting | Guidance |
| --- | --- |
| `[reader] address`, `source` | one tagstrak process per reader; `source` labels its observations |
| `[runtime] queue_capacity` | size for the largest burst of tags the reader can report faster than tagstrak processes them; overflow fails the run, so err high (1 to 100,000; each slot is 64 bytes) |
| `[report]` | per-destination queues, batching, and retry budgets; see [report.md](report.md) |
| `[telemetry] address` | read-only health and metrics; keep it on loopback or an operations network, never public |
| `[control]` | omit to disable; a Unix socket in a 0700 directory, such as the systemd `RuntimeDirectory`, or loopback TCP with a token; see [security.md](security.md) |
| `[subscription_store]` | `file` keeps subscriptions in the configuration file; `api` keeps them in a 0600 state file, such as one under the systemd `StateDirectory`, replaced through the management API |
| `[filter]` | leave the default prefix index; adaptive mode is experimental ([filter.md](filter.md)) |

Secrets, such as destination tokens, come only from environment variables or files, never from the configuration file itself.

## Monitor

Point Prometheus at `http://<telemetry>/metrics` and an orchestrator's probes at `/healthz` and `/readyz`.
Alert on:

- `tagstrak_ready` at 0 for longer than a reconnect cycle
- `tagstrak_reader_failures_total` and `tagstrak_reader_reconnects_total` rising
- `tagstrak_queue_depth` approaching `tagstrak_queue_capacity`
- `tagstrak_report_dropped_total`, `tagstrak_report_failed_total`, or `tagstrak_report_undelivered_total` increasing for any destination
- `tagstrak_observations_untranslatable_total` rising, which usually means tags in schemes tagstrak does not support

## Migrating from v1

v2 is a rewrite with the same purpose, not a drop-in upgrade.
v1 flags and behaviour are not compatibility contracts.

| v1 | v2 |
| --- | --- |
| `gosstrak start --ip ADDR` | `tagstrak --reader HOST:PORT`, or `[reader] address` |
| `--ecspecfile ecspec.csv` with `reportURI,urn:epc:pat:…` rows | `[[subscriptions]]` with `id`, `patterns`, and `destinations` in the configuration file |
| report URIs in the ECSpec, never delivered to | `[[destinations]]` that receive JSON batches over HTTP |
| `--managementAddr` pseudo-ALE endpoint | optional `[control]` management API |
| `--enableStat` and InfluxDB flags | `/metrics` in Prometheus text format |
| filtering engines chosen and swapped at run time | the prefix index by default; adaptive mode is opt-in and experimental |
| `/var/tmp/gosstrak-fc-cache` | no on-disk cache; with `source = "api"`, one state file |
| `cmd/ale-ec`, InfluxDB monitoring | removed |

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

A v2 pattern names every field of its scheme, so v1's shorter prefix patterns gain `.*` for each field they omit; a pattern that does not compile stops startup with the field that is wrong.
Use `urn:epc:idpat:` patterns to match an identity whatever its filter value or tag length.
See [tdt.md](tdt.md) for the supported schemes and pattern syntax.

The public `llrp` package also changed: v1 builder functions were removed, and the codec API is described in [llrp.md](llrp.md).

## Container image

`build/tagstrak-rig/Dockerfile` builds the tagstrak-rig reader emulator, which is useful for staging and tests.
No image is published for tagstrak; build the binary as above.
