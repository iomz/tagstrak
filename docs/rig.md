# tagstrak-rig

tagstrak-rig is the test rig that comes with tagstrak: an LLRP reader emulator that plays deterministic scenarios, and a tag data generator.
Issue #6 replaced the legacy golemu emulator with deterministic scenarios and one context-owned runtime for both workflows.
`internal/emulator` owns scenario progression and LLRP serving.
`internal/app/rig` composes the data plane with optional local control.
The public `llrp` codec handles every frame, while `internal/inventory` owns EPC validation and inventory snapshots.

## Run a repeating inventory

From the repository root:

```sh
go run ./cmd/tagstrak-rig server \
  --inventory examples/rig/inventory.json \
  --listen 127.0.0.1:5084 \
  --report-interval 1s
```

Connect the reader runtime in another terminal:

```sh
go run ./cmd/tagstrak \
  --reader 127.0.0.1:5084 \
  --telemetry 127.0.0.1:8080
```

tagstrak exposes its counts at `/metrics` and its reader state at `/readyz`; without a configuration file it matches and delivers nothing, so the counts show tags arriving and being translated ([runtime.md](runtime.md)).

`server` loads an existing version-1 [inventory file](inventory.md) and turns it into one repeating cycle.
Missing or invalid files fail startup.
`--shuffle --seed 42` enables deterministic per-cycle ordering; without shuffle, EPCs are sorted.

## Run a scenario

```sh
go run ./cmd/tagstrak-rig simulator \
  --scenario examples/rig/scenario.json \
  --listen 127.0.0.1:5084 \
  --report-interval 1s
```

The example represents arrivals, departures, and an empty inventory: SGTIN products 0614141.812345.1 to 3, an SSCC pallet, and one unrelated product, 0614141.900001.7.
It pairs with `examples/tagstrak/tagstrak.toml`, whose subscriptions match the products and the pallet but not the unrelated product.

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

`version`, `seed`, and `cycles` are required.
`seed` is a signed 64-bit integer; `repeat` and `shuffle` default to false.
Unknown fields, repeated fields, null fields/cycles, trailing data, duplicate EPCs within a cycle, and malformed EPCs are rejected.
EPCs may recur in different cycles.

Both commands use the same listener, handshake, timers, report encoder, cancellation, and client limits.
The `client` mode, old CLI flags, gob scenarios, directory-based simulator, and unauthenticated TCP mutation API have been removed.
Use tagstrak to consume reports.

## Generate tag data

`tagstrak-rig uii` prints one generated tag as `PC,EPC` in hex, ready for an inventory or scenario file:

```sh
go run ./cmd/tagstrak-rig uii epc --scheme sgtin-96 --company-prefix 0614141 --item-ref 812345 --serial 6789
# 3000,3074257bf7194e4000001a85
```

`epc` takes `--scheme` (`sgtin-96`, `sscc-96`, `giai-96`, or `grai-96`), `--filter`, `--company-prefix`, and the scheme's fields: `--item-ref` and `--serial` for SGTIN, `--serial-ref` for SSCC, `--asset-type` and `--serial` for GRAI, and `--asset-ref` for GIAI.
Fields left out are generated at random.
`iso17360` generates an ISO/IEC 17360 transport unit from `--agency`, `--company`, `--serial`, and `--data-identifier`; `iso17363` takes an ISO 6346 container code as `--owner`, `--category`, and `--serial`, validates it and its check digit, but refuses to write it, because its UII carriage is not established ([tdt.md](tdt.md)).

`--format bin` prints the EPC as bits.
`--pattern` prints a subscription pattern and the bit prefix it pins instead; the first field left out and every field after it become stars:

```sh
go run ./cmd/tagstrak-rig uii epc --scheme sscc-96 --company-prefix 0614141 --pattern
# urn:epc:pat:sscc-96:3.0614141.*,00110001011101000010010101111011111101
```

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
control_dir=$(mktemp -d /tmp/tagstrak-rig.XXXXXX)
go run ./cmd/tagstrak-rig simulator \
  --scenario examples/rig/scenario.json \
  --control-socket "$control_dir/control.sock"
```

From another terminal, substitute the created directory:

```sh
curl --unix-socket /tmp/tagstrak-rig.EXAMPLE/control.sock \
  http://localhost/status

curl --unix-socket /tmp/tagstrak-rig.EXAMPLE/control.sock \
  -X PUT -H 'Content-Type: application/json' \
  --data-binary @examples/rig/scenario.json \
  http://localhost/scenario
```

`GET /status` returns bound data address, running state, active/accepted/rejected clients, failures, completed cycles, reports, keepalives, and scenario revision.
`PUT /scenario` requires `Content-Type: application/json` and returns 204 after validation and publication, 400 with a structured error without changing state, or 415 for another content type.
Each replacement attempt is audited with the scenario revision and cycle count, never the scenario.
A one-cycle repeating scenario updates a server inventory through the same operation.
All control I/O runs on the separate Unix socket; LLRP sockets cannot mutate scenarios.
The transport is shared with tagstrak's management API; see [control.md](control.md).

## Protocol and resource budgets

The supported handshake matches `internal/reader`: successful reader notification, `SET_READER_CONFIG`, then successful config response echoing the request ID.
The emulator accepts an optional periodic `KeepaliveSpec` and requires correctly correlated, empty-payload ACKs.
Unsupported/reset configurations, invalid parameters, unexpected messages, wrong ACKs, malformed frames, and missing ACKs close that client.
A peer-requested keepalive interval must fit below the configured read timeout minus ACK timeout.
ROSpec management and physical RF propagation are outside this emulator subset.

| Resource | Default | Flag | Bound or behavior |
| --- | --- | --- | --- |
| LLRP clients | 8 | `--max-clients` | Configurable 1–128; excess clients close immediately |
| Inbound queue | 1 frame per client | fixed | Reader blocks when full; no outbound queue |
| Frame bytes | 1,500 | `--frame-bytes` | Configurable 128–1,048,576; shared codec rejects larger input before allocation |
| Report interval | 1 second | `--report-interval` | Delay between completed cycles |
| Keepalive interval | 10 seconds | `--keepalive-interval` | Default if omitted by peer; next keepalive follows ACK |
| ACK timeout | 5 seconds | `--ack-timeout` | Closes clients that do not acknowledge |
| Handshake timeout | 5 seconds | `--handshake-timeout` | Absolute deadline across notification, request, and response |
| Read timeout | 30 seconds | `--read-timeout` | Absolute deadline for each inbound frame, including partial input |
| Write timeout | 5 seconds | `--write-timeout` | Per control frame or entire report cycle, not per fragment |
| Scenario input | 1 MiB | fixed | Applies to simulator files and control bodies |
| Inventory input | 16 MiB | fixed | Existing inventory loader budget |
| Scenario cycles | 1,024 | fixed | At least one cycle is required |
| Total scenario tag records | 100,000 | fixed | Across all cycles; EPCs contain 2–62 even bytes |
| Control connections | 4 | fixed | Excess connections close without a waiting queue |
| HTTP deadlines | 2-second headers, 5-second read/write/idle | fixed | Header budget 8 KiB |
| Control shutdown | 5 seconds | `--shutdown-timeout` | Configurable 10 ms–1 minute; force-close after deadline |

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
go build ./cmd/tagstrak-rig ./cmd/tagstrak
```

Tests cover deterministic wire reports, seed isolation, strict configuration, shared reader interoperability, frame splitting, cycle-boundary replacement, real socket deadlines, keepalives/ACK failure, client limits, control access, startup failure, disconnects, and cancellation cleanup.
