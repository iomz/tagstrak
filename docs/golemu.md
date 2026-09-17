# Deterministic golemu

Issue #6 replaces the legacy golemu implementation with deterministic scenarios and one context-owned runtime for both workflows.
`internal/emulator` owns scenario progression and LLRP serving.
`internal/app/golemu` composes the data plane with optional local control.
The public `llrp` codec handles every frame, while `internal/inventory` owns EPC validation and inventory snapshots.

## Run a repeating inventory

From the repository root:

```sh
go run ./cmd/golemu server \
  --inventory examples/golemu/inventory.json \
  --listen 127.0.0.1:5084 \
  --report-interval 1s
```

Connect the reader runtime in another terminal:

```sh
go run ./cmd/gosstrak start \
  --reader 127.0.0.1:5084 \
  --telemetry 127.0.0.1:8080
```

The reader exposes ingestion counts at `/metrics` and capability at `/readyz`.
Gosstrak's current ingestion-only scope remains as documented in [runtime.md](runtime.md).

`server` loads an existing version-1 [inventory file](inventory.md) and turns it into one repeating cycle.
Missing or invalid files fail startup.
`--shuffle --seed 42` enables deterministic per-cycle ordering; without shuffle, EPCs are sorted.

## Run a scenario

```sh
go run ./cmd/golemu simulator \
  --scenario examples/golemu/scenario.json \
  --listen 127.0.0.1:5084 \
  --report-interval 1s
```

The copyable example represents arrivals, departures, and an empty inventory:

```json
{
  "version": 1,
  "seed": 42,
  "repeat": true,
  "shuffle": true,
  "cycles": [
    ["303400000000000000000001", "303400000000000000000002"],
    ["303400000000000000000002", "303400000000000000000003"],
    []
  ]
}
```

`version`, `seed`, and `cycles` are required.
`seed` is a signed 64-bit integer; `repeat` and `shuffle` default to false.
Unknown fields, repeated fields, null fields/cycles, trailing data, duplicate EPCs within a cycle, and malformed EPCs are rejected.
EPCs may recur in different cycles.

Both commands use the same listener, handshake, timers, report encoder, cancellation, and client limits.
The `client` mode, old CLI flags, gob scenarios, directory-based simulator, and unauthenticated TCP mutation API have been removed.
Use gosstrak to consume reports.

## Reproducibility and updates

Each connection starts at cycle zero with its own seeded random generator.
Input EPCs are canonicalized before optional Fisher–Yates shuffling, so input array ordering does not change results.
The RNG continues across repeated passes through the scenario.
A reconnect starts the scenario and RNG again; concurrent clients do not share cursor or message-ID state.

The first cycle is sent immediately after successful configuration.
Subsequent cycles start one report interval after the previous cycle finishes, with no catch-up bursts or skipped scenario cycles.
Empty cycles produce an empty `RO_ACCESS_REPORT`.
A finite scenario stops emitting reports after its final cycle but continues keepalives until cancellation, disconnect, or a scenario update.

For the same scenario, seed, frame budget, and supported implementation, the `RO_ACCESS_REPORT` byte sequence is identical.
Report IDs start at 1000 per connection and do not share a counter with notifications or keepalives.
Wall-clock notification timestamps and the interleaving of keepalives with reports are outside this guarantee.
The injected `Clock` controls timestamps and pacing; socket deadlines use real time even when the scenario clock is paused.
Injected random factories return fresh per-cursor generators; their calls and shared clock methods must be safe concurrently.

Control publishes a whole immutable scenario atomically.
An in-flight cycle finishes all report fragments from its existing snapshot.
At the next cycle boundary, each client observes the latest revision and resets its cycle cursor and RNG to the new scenario's seed.
Report IDs continue increasing within the connection.
Clients can reach that boundary at different wall-clock times, and multiple updates before a boundary collapse to the latest revision.
The emulator does not overwrite input files or persist control updates.
Replay the same update sequence at the same cycle boundaries when reproducing an updated run.

## Local control

Control is disabled unless `--control-socket` is supplied.
The socket path must be absolute, and its existing parent directory must be private with no group or other permissions.
The socket is created with mode 0600; existing paths are never removed or replaced.
The application removes only its own Unix socket on normal shutdown.
This boundary trusts processes running as the same local user; it is not a remote management or multi-user authorization API.

```sh
control_dir=$(mktemp -d /tmp/golemu-control.XXXXXX)
go run ./cmd/golemu simulator \
  --scenario examples/golemu/scenario.json \
  --control-socket "$control_dir/control.sock"
```

From another terminal, substitute the created directory:

```sh
curl --unix-socket /tmp/golemu-control.EXAMPLE/control.sock \
  http://localhost/status

curl --unix-socket /tmp/golemu-control.EXAMPLE/control.sock \
  -X PUT -H 'Content-Type: application/json' \
  --data-binary @examples/golemu/scenario.json \
  http://localhost/scenario
```

`GET /status` returns bound data address, running state, active/accepted/rejected clients, failures, completed cycles, reports, keepalives, and scenario revision.
`PUT /scenario` returns 204 after validation and publication, or 400 without changing state.
A one-cycle repeating scenario updates a server inventory through the same operation.
All control I/O runs on the separate Unix socket; LLRP sockets cannot mutate scenarios.
Authenticated network control remains a separate milestone concern.

## Protocol and resource budgets

The supported handshake matches `internal/reader`: successful reader notification, `SET_READER_CONFIG`, then successful config response echoing the request ID.
The emulator accepts an optional periodic `KeepaliveSpec` and requires correctly correlated, empty-payload ACKs.
Unsupported/reset configurations, invalid parameters, unexpected messages, wrong ACKs, malformed frames, and missing ACKs close that client.
A peer-requested keepalive interval must fit below the configured read timeout minus ACK timeout.
ROSpec management and physical RF propagation are outside this emulator subset.

| Resource | Default | Bound or behavior |
| --- | --- | --- |
| LLRP clients | 8 | Configurable 1–128; excess clients close immediately |
| Inbound queue | 1 frame per client | Reader blocks when full; no outbound queue |
| Frame bytes | 1,500 | Configurable 128–1,048,576; shared codec rejects larger input before allocation |
| Report interval | 1 second | Delay between completed cycles |
| Keepalive interval | 10 seconds | Default if omitted by peer; next keepalive follows ACK |
| ACK timeout | 5 seconds | Closes clients that do not acknowledge |
| Handshake timeout | 5 seconds | Absolute deadline across notification, request, and response |
| Read timeout | 30 seconds | Absolute deadline for each inbound frame, including partial input |
| Write timeout | 5 seconds | Per control frame or entire report cycle, not per fragment |
| Scenario input | 1 MiB | Applies to simulator files and control bodies |
| Inventory input | 16 MiB | Existing inventory loader budget |
| Scenario cycles | 1,024 | At least one cycle is required |
| Total scenario tag records | 100,000 | Across all cycles; EPCs contain 2–62 even bytes |
| Control connections | 4 | Excess connections close without a waiting queue |
| HTTP deadlines | 2-second headers, 5-second read/write/idle | Header budget 8 KiB |
| Control shutdown | 5 seconds | Configurable 10 ms–1 minute; force-close after deadline |

All emulator durations must be 10 ms–1 hour.
Read timeout must exceed the default keepalive interval plus ACK timeout.
A report cycle holds one immutable tag snapshot and one bounded frame payload; it never builds an unbounded report queue.
Frame splitting includes the exact ten-byte LLRP envelope and enforces the public codec's parameter-count limit.

Each connection has one writer, one read worker, and one cancellation watcher.
The listener caps client workers before starting them.
Cancellation closes sockets to interrupt blocked reads/writes, stops timers, and joins all connection workers.
Client failure is isolated; listener or control-server failure cancels the application.
Normal SIGINT/SIGTERM exits successfully after cleanup.

## Design evaluation

Assumptions: deterministic logical inventory cycles, cooperative injected clocks/random sources/loggers, and single-process local control.
A per-connection cursor makes replay independent of connection order; a global simulation ticker would couple clients to wall time and each other.
A single socket writer avoids interleaved frames; the one-frame inbound queue lets it handle ACKs while pacing reports without adding an outbound buffering layer.
Whole-scenario replacement avoids partially applied tag mutations and reuses immutable inventory identities.

Tradeoffs: replacement restarts RNG/cycle progression at a boundary, input files remain unchanged, and private Unix control targets Linux/macOS rather than remote clients.
Per-tag HTTP mutation, persistent scenario editing, a generic event bus, and a separate simulation server would add state/ownership contracts without helping this milestone's replay requirements.
The clock and random interfaces are narrow test seams backed by standard-library implementations.
No new third-party dependency is introduced; obsolete Gin/logrus dependencies are removed with their legacy runtime.

## Verification

```sh
go test -race ./...
go vet ./...
go build ./cmd/golemu ./cmd/gosstrak ./cmd/tagjson
```

Tests cover deterministic wire reports, seed isolation, strict configuration, shared reader interoperability, frame splitting, cycle-boundary replacement, real socket deadlines, keepalives/ACK failure, client limits, control access, startup failure, disconnects, and cancellation cleanup.
