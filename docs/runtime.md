# Reader runtime

Issue #5 implements the tagstrak ingestion lifecycle in the v2 milestone tracked by #1.
The architecture decision in #2 keeps tagstrak and tagstrak-rig as separate processes with direct calls inside each process and bounded queues at ownership boundaries.

## Packages and scope

- `internal/reader` owns one reader connection at a time, handshake validation, frame deadlines, observation decoding, and reconnect policy.
- `internal/app/tagstrak` owns the reader, observation consumer, queue, and read-only HTTP telemetry server.
- `cmd/tagstrak` parses inputs, supplies dependencies, creates the signal context, and selects the final process exit status.
- `llrp` owns all wire framing and parameter decoding; it does not dial, retry, log, or close connections.
- `internal/inventory.Observation` is the immutable domain handoff already established by #4.

The runtime validates observations, translates them, matches them against subscriptions, delivers reports, and exposes accepted/processed/translated counts.
The command's consumer is `internal/app/tagstrak.TranslatingConsumer`, which adapts each observation to the #8 translation boundary described in [tdt.md](tdt.md).
Its processor, `MatchingProcessor`, matches each translation against the current subscriptions, compiled as described in [filter.md](filter.md), and logs the identity and matched subscription IDs at debug level.
Configured with `DeliverTo`, it also enqueues one report event per routed destination; [report.md](report.md) describes delivery.
Tags outside the supported standards are counted as untranslatable and dropped; any other translation error fails the consumer.
Tags whose reader reports no protocol-control word are translated as GS1 EPC data by explicit configuration, never by guessing from the header.
Subscriptions, destinations, and routes come from the `--config` file described in [configuration.md](configuration.md); without one the command matches nothing and delivers nothing.
It does not retain observations.
The old global command runtime, SPDY listener, adaptive engine workers, and InfluxDB worker wiring are removed, together with their legacy packages (#11); they remain readable at the `v1.0.0` tag.
The emulator lifecycle and scenarios implemented by #6 are documented in [rig.md](rig.md).

## Run

```sh
go run ./cmd/tagstrak \
  --reader 127.0.0.1:5084 \
  --source reader-1 \
  --telemetry 127.0.0.1:8080 \
  --max-retries 5
```

Use `--help` for all supported flags.
Settings apply in order: defaults, the `--config` file, `TAGSTRAK_*` environment variables, then flags; see [configuration.md](configuration.md).
V1 flags and behavior are not compatibility contracts.

| Budget | Default | Setting | Behavior |
| --- | --- | --- | --- |
| Connect timeout | 5 seconds | `--connect-timeout`, `[reader] connect_timeout` | Bounds each dial, including DNS through `net.Dialer` |
| Handshake timeout | 5 seconds | `--handshake-timeout`, `[reader] handshake_timeout` | One absolute deadline for notification, config write, and response, including intervening keepalives |
| Read timeout | 30 seconds | `--read-timeout`, `[reader] read_timeout` | Bounds each complete frame, including a stalled partial payload |
| Write timeout | 5 seconds | `--write-timeout`, `[reader] write_timeout` | Bounds each complete config or keepalive ACK write |
| Retry delays | 1–30 seconds | `--retry-min`, `--retry-max`, `[reader] retry_min`, `retry_max` | Deterministic exponential backoff capped at the maximum |
| Reconnect budget | 5 | `--max-retries`, `[reader] max_retries` | At most 6 connection attempts for the entire run; successful handshakes do not reset it |
| Frame limit | 1 MiB | `--max-frame-bytes`, `[reader] max_frame_bytes` | Rejects oversized advertised lengths before proportional allocation |
| Parameter size/count | 32 KiB / 4,096 | `--max-parameter-bytes`, `--max-parameters`, `[reader] max_parameter_bytes`, `max_parameters` | Public codec limits per parameter/container, with total input bounded by frame size |
| Observation queue | 128 | `--queue-capacity`, `[runtime] queue_capacity` (1–100,000) | Full queue fails the run immediately; no hidden queue or retry |
| Consumer timeout | 5 seconds | `--consume-timeout`, `[runtime] consume_timeout` | Context deadline for each observation |
| Shutdown timeout | 10 seconds | `--shutdown-timeout`, `[runtime] shutdown_timeout` | Shared queue-drain and HTTP shutdown deadline |
| Telemetry connections | 32 | fixed | Excess accepted connections close immediately |
| HTTP headers | 8 KiB | fixed | Header budget plus 2-second header and 5-second request/write deadlines |

Queue capacity is bounded to 1–100,000 observations.
Queue memory contains immutable EPC values of at most 62 bytes and bounded metadata; at most one decoded report batch is held alongside it.
Size the queue for the expected burst and consumer rate.
Overflow is an explicit failure, not a durable-delivery or replay mechanism.

## Session protocol and states

```text
idle -> connecting -> handshaking -> serving
            |              |           |
            +--------------+-----------+-> reconnecting -> connecting
                                       |
                                       +-> keepalive -> serving
                                       +-> reporting -> serving

Any active state -> stopping -> stopped (cancellation)
Any terminal failure -> stopping -> failed
```

The reader must first send a successful connection-attempt notification.
Tagstrak sends `SET_READER_CONFIG` and accepts exactly one successful LLRP status in the response with the matching request ID.
The supported config helper requests a 10-second keepalive interval.
Keepalive ACKs echo the incoming keepalive ID and can be exchanged during handshake without extending its deadline.
Once serving, valid notifications, keepalives, and reports are accepted; unsupported message types terminate the run.
Read deadlines detect silent or stalled peers; reports also demonstrate connection activity.
This subset consumes reports from the reader's configured inventory operation and does not create ROSpecs.

Protocol errors, rejected configuration, invalid EPCs, and queue overflow are terminal.
EOF, truncated transport reads, connection errors, and network timeouts may reconnect within the configured budget.
Each reconnect starts a fresh handshake and readiness becomes false until it succeeds.
Readiness includes the transient keepalive and reporting states of an established session.

Every report is validated before any observation from it is handed off.
Each tag report requires one EPC, represented as whole 16-bit words within the domain's 2–62-byte range.
Repeated, missing, non-word, or incorrectly sized EPC values fail the report.
Observation timestamps are local receipt times; source is the configured reader identity.
PC remains a wire concern and does not alter tag identity.

## Ownership and shutdown

Constructors validate without I/O or goroutines.
`Session.Run` and `App.Run` are single-use.
The reader owns its connections and joins its connection-cancellation watcher before returning.
The app owns the bounded observation queue and closes it only after reader exit.
The consumer runs in one app-owned worker; errors stop ingestion and fail the app.

On cancellation, ingestion stops first and accepted observations drain through the consumer.
The shared shutdown deadline cancels an unfinished consumer and forces remaining HTTP connections closed.
Abandoned observations are counted and a missed drain deadline returns `ErrDrainDeadline`.
With `WithDelivery`, report delivery starts before ingestion and drains after the consumer, within the remaining shared deadline; undelivered reports are returned as `report.ErrUndelivered`.
Every app worker is joined before `Run` returns.
Injected `Dialer` and `Consumer` implementations must honor context cancellation; Go cannot forcibly terminate an arbitrary non-cooperative function.
Injected log handlers must complete promptly and must not start unowned workers.
The production consumer performs no blocking I/O.

Normal SIGINT/SIGTERM shutdown exits successfully.
Protocol failures, exhausted retries, consumer failures, and incomplete shutdown, including undelivered reports, exit nonzero.
A supervisor may restart a failed process, but that external restart policy is separate from the reader's finite retry budget.

## Telemetry

- `GET /healthz`: JSON process liveness, with HTTP 503 before running or after shutdown.
- `GET /readyz`: JSON serving capability, with HTTP 503 during connection, handshake, reconnect, failure, or drain.
- `GET /metrics`: Prometheus text exposition, with `# HELP` and `# TYPE` for every family, as listed below.

Report families appear once destinations are configured, and `tagstrak_filter_adaptive_*` families only in adaptive filter mode, where they restart from zero when subscriptions are replaced.

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

Status includes reader state, last connection error, and the actual bound telemetry address.
Health remains live during reconnect and drain; readiness describes the reader session only, and delivery state does not affect it ([report.md](report.md)).
State and connection-failure logs use structured `slog` fields.
Per-frame state changes are debug-level to avoid turning tag throughput into an info-log flood.
The telemetry server defaults to loopback and has no mutation endpoints or authentication.
Bind it to a trusted operations network; authenticated management is the separate control listener in [control.md](control.md), which binds before the reader dials and shuts down within the same deadline.

## Design evaluation

Assumptions: one reader session per app, immutable observations from #4, finite resource budgets, and cooperative injected consumers.
One reader loop plus one consumer worker keeps network ownership explicit while allowing a small burst buffer.
A fully synchronous consumer would let processing stall keepalive handling; an event bus or separate read/write worker queues would add ownership, ordering, and shutdown costs without a current requirement.
The bounded channel is therefore the only observation concurrency boundary.

The overflow policy favors visible failure over silent loss, at the cost of terminating a session when bursts exceed capacity.
Deterministic backoff gives reproducible transitions; large fleets needing jitter require a separately specified policy.
Strict message correlation rejects legacy peers that emit unrelated response IDs, which is why the local emulator response is fixed here.
The small handwritten handshake validates the supported subset using public codec primitives and avoids a second framing implementation or an FSM dependency.
Maintenance stays within standard-library runtime code and the existing domain/codec contracts.

## Verification

```sh
go test -race ./...
go vet ./...
go build ./cmd/tagstrak ./cmd/tagstrak-rig
git diff --check
```

Runtime tests cover deterministic state order, TCP disconnect/reconnect, finite retry budgets across successful handshakes, dial/backoff cancellation, handshake/partial-read/write timeouts, handshake keepalive deadlines, malformed/oversized frames, rejected status/IDs/EPCs, queue overflow, consumer failure, bounded drain, readiness, and HTTP listener shutdown.
