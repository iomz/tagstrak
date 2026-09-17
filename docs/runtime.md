# Reader runtime

Issue #5 implements the gosstrak ingestion lifecycle in the v2 milestone tracked by #1.
The architecture decision in #2 keeps gosstrak and golemu as separate processes with direct calls inside each process and bounded queues at ownership boundaries.

## Packages and scope

- `internal/reader` owns one reader connection at a time, handshake validation, frame deadlines, observation decoding, and reconnect policy.
- `internal/app/gosstrak` owns the reader, observation consumer, queue, and read-only HTTP telemetry server.
- `cmd/gosstrak` parses inputs, supplies dependencies, creates the signal context, and selects the final process exit status.
- `llrp` owns all wire framing and parameter decoding; it does not dial, retry, log, or close connections.
- `internal/inventory.Observation` is the immutable domain handoff already established by #4.

This milestone slice runs in **ingestion mode**: it validates observations and exposes accepted/processed counts.
The command's consumer acknowledges ingestion only; it does not retain observations or claim translation, matching, or destination delivery.
Issues #7–#9 supply those downstream capabilities through the consumer boundary.
The old global command runtime, SPDY listener, adaptive engine workers, and InfluxDB worker wiring are removed from the command.
Their legacy packages remain available to their own workstreams.
The emulator lifecycle and scenarios implemented by #6 are documented in [golemu.md](golemu.md).

## Run

```sh
go run ./cmd/gosstrak start \
  --reader 127.0.0.1:5084 \
  --source reader-1 \
  --telemetry 127.0.0.1:8080 \
  --max-retries 5
```

`start` is optional.
Use `--help` for all supported flags.
Configuration uses explicit command flags and validated defaults in this slice; shared file/environment configuration remains operations work in #10.
V1 flags and behavior are not compatibility contracts.

| Budget | Default | Behavior |
| --- | --- | --- |
| Connect timeout | 5 seconds | Bounds each dial, including DNS through `net.Dialer` |
| Handshake timeout | 5 seconds | One absolute deadline for notification, config write, and response, including intervening keepalives |
| Read timeout | 30 seconds | Bounds each complete frame, including a stalled partial payload |
| Write timeout | 5 seconds | Bounds each complete config or keepalive ACK write |
| Retry delays | 1–30 seconds | Deterministic exponential backoff capped at the maximum |
| Reconnect budget | 5 | At most 6 connection attempts for the entire run; successful handshakes do not reset it |
| Frame limit | 1 MiB | Rejects oversized advertised lengths before proportional allocation |
| Parameter size/count | 32 KiB / 4,096 | Public codec limits per parameter/container, with total input bounded by frame size |
| Observation queue | 128 | Full queue fails the run immediately; no hidden queue or retry |
| Consumer timeout | 5 seconds | Context deadline for each observation |
| Shutdown timeout | 10 seconds | Shared queue-drain and HTTP shutdown deadline |
| Telemetry connections | 32 | Excess accepted connections close immediately |
| HTTP headers | 8 KiB | Header budget plus 2-second header and 5-second request/write deadlines |

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
Gosstrak sends `SET_READER_CONFIG` and accepts exactly one successful LLRP status in the response with the matching request ID.
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
Every app worker is joined before `Run` returns.
Injected `Dialer` and `Consumer` implementations must honor context cancellation; Go cannot forcibly terminate an arbitrary non-cooperative function.
Injected log handlers must complete promptly and must not start unowned workers.
The production consumer performs no blocking I/O.

Normal SIGINT/SIGTERM shutdown exits successfully.
Protocol failures, exhausted retries, consumer failures, and incomplete shutdown exit nonzero.
A supervisor may restart a failed process, but that external restart policy is separate from the reader's finite retry budget.

## Telemetry

- `GET /healthz`: JSON process liveness, with HTTP 503 before running or after shutdown.
- `GET /readyz`: JSON serving capability, with HTTP 503 during connection, handshake, reconnect, failure, or drain.
- `GET /metrics`: Prometheus text counters and gauges for attempts, reconnects, frames, reports, keepalives, failures, queue overflow, accepted/processed/abandoned observations, queue depth/capacity, readiness, and current retry delay.

Status includes reader state, last connection error, and the actual bound telemetry address.
Health remains live during reconnect and drain; readiness represents ingestion capability only in this milestone slice.
State and connection-failure logs use structured `slog` fields.
Per-frame state changes are debug-level to avoid turning tag throughput into an info-log flood.
The telemetry server defaults to loopback and has no mutation endpoints or authentication.
Bind it to a trusted operations network; authenticated management is separate work.

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
go build ./cmd/gosstrak ./cmd/golemu ./cmd/tagjson
git diff --check
```

Runtime tests cover deterministic state order, TCP disconnect/reconnect, finite retry budgets across successful handshakes, dial/backoff cancellation, handshake/partial-read/write timeouts, handshake keepalive deadlines, malformed/oversized frames, rejected status/IDs/EPCs, queue overflow, consumer failure, bounded drain, readiness, and HTTP listener shutdown.
