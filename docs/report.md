# Report delivery

Issue #9 adds bounded report delivery in the v2 milestone tracked by #1.
The [#9 design record](https://github.com/iomz/tagstrak/issues/9#issuecomment-6377429118) is the decision source.

## Packages and scope

| Package | Imports | Owns |
| --- | --- | --- |
| `internal/report` | standard library | `Event`, `Batch`, `Reporter`, error classes, `Dispatcher`, the JSON wire schema, stats |
| `internal/report/httpreport` | `report` | HTTP POST adapter |
| `internal/app/tagstrak` | `report`, `filter`, `tdt` | `EventOf`, `Routes`, `DeliverTo`, and the `WithDelivery` lifecycle |

`report` imports no identity or translator types; `EventOf` renders events at the application boundary, as `TranslationInput` and `SubjectOf` do for translation and matching.
This amends #2, which had `report` import `tag` and `identity`.

```text
consumer worker: translate → match → route → Dispatcher.Enqueue (never blocks)
per destination: bounded queue → batch → Reporter.Deliver → bounded retry
```

## Delivery contract

- **Guarantee:** at-least-once for events accepted into a queue, while the process runs and the destination recovers within the retry budget. Nothing survives a crash, and exactly-once is not offered.
- **Duplicates:** a retry after a timeout can repeat a batch the destination already committed. Receivers deduplicate on the event `id`; the HTTP adapter also sends the batch ID as `Idempotency-Key`.
- **Ordering:** FIFO per destination. One batch is in flight per destination and retries hold that destination's queue. There is no ordering across destinations.
- **Loss:** only on queue overflow, final failure, crash, or the shutdown deadline. Overflow and final failures are counted per destination and logged; the shutdown case returns `report.ErrUndelivered` from `App.Run`, so the process exits nonzero. None is silent.
- **Isolation:** each destination has its own queue and worker, so a slow destination never delays another or the reader.

## Bounds

| Budget | Default | Behaviour |
| --- | --- | --- |
| Queue capacity | 10,000 events per destination | `Enqueue` never blocks; a full queue drops the new event, counts it, and logs once per overflow episode |
| Batch | 500 events or 1 MiB encoded | Whichever is reached first closes the batch; a single larger event goes alone |
| Flush interval | 1 s | A partial batch is sent once its first event is this old |
| Request timeout | 5 s | Per attempt |
| Retry | 5 attempts, backoff 200 ms doubling to 10 s | Deterministic; `Retry-After` is honoured up to the cap |
| Retry budget | 30 s per batch | After that the batch is a final failure |
| Drain | the app's `ShutdownTimeout` | Shared with the consumer drain |

`Config.Validate` rejects zero, negative, and unbounded values, including upper limits on queue size, batch bytes, attempts, and retry budget.

A `Reporter` returns `nil`, a retryable error, or `report.Permanent(err)`.
A permanent error, exhausted attempts, or an exhausted retry budget is a final failure, logged with destination, batch ID, event count, and first and last event IDs.

## HTTP adapter

- One batch per `POST`, `Content-Type: application/json`, body `Batch.JSON()`, `Idempotency-Key` set to the batch ID.
- 2xx is success. 408, 425, 429, 5xx, and transport errors are retryable. Every other status is permanent.
- Redirects are never followed, so request credentials cannot reach another host; a redirect is a permanent failure.
- TLS certificates are always verified against the system roots, with no option to skip verification. This is tagstrak's first outbound TLS path, so the patched Go toolchain and CA bundle from #20 apply to it.
- Destination URLs never appear in errors, logs, or metrics, because their user information, path, or query may carry credentials. Destinations are named, and the name is the label; transport errors may name the host and port.
- Credentials belong in `WithHeader`, never in the URL. The configuration file supplies them as redacted header secrets from environment or file sources ([configuration.md](configuration.md)).

## Wire schema

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

- One event per observation and destination, listing that destination's matched subscriptions in subscription order. Events for one observation share an `id` across destinations.
- IDs are `<instance>:<sequence>`, with a random instance per process start, so they stay unique across restarts without persistence.
- `pureIdentityURI` is omitted when no standards-backed form exists. ISO identities are reported through `identity.components` only, because `iso.Display` is not an interchange form (#8).
- Additive fields keep `tagstrak.report.v1`; changing a field's meaning needs a new schema name.

## Routing and configuration

`Routes` maps subscription IDs to destination names, and `DeliverTo` rejects routes to unknown destinations.
Subscriptions, destinations, and routes come from the configuration file ([configuration.md](configuration.md)); without one, `cmd/tagstrak` delivers nothing.
The end-to-end path is exercised in-process by `internal/e2e`.

## Telemetry

`/metrics` exposes, per destination, `tagstrak_report_{enqueued,delivered,dropped,failed,undelivered,retries}_total` and `tagstrak_report_queue_{depth,capacity}` with a `destination` label.
The same counters appear under `delivery` in `/healthz` and `/readyz`.
Delivery state does not affect readiness, because readiness describes ingestion.

## Design evaluation

Assumptions: destinations are HTTP endpoints that acknowledge with a status code, and losing reports on crash is acceptable until a durable store has a requirement.
A per-destination queue keeps one destination's outage from blocking the reader, at the cost of dropping new events once that destination's queue is full; blocking or failing the process would turn one destination's outage into an ingestion outage.
Head-of-line retry keeps per-destination order at the cost of throughput during an outage.
HTTP is the only adapter: it maps cleanly onto the retry and status model and needs no third-party client. JSON Lines output and message brokers are later adapters; v1's SPDY transport is not carried forward.

## Verification

```sh
go test -race ./internal/report/... ./internal/app/... ./internal/e2e/...
go vet ./...
```

Tests cover batching by count, bytes, and age; FIFO order; drop-newest overflow and its logging; deterministic backoff and `Retry-After`; permanent, attempts-exhausted, and budget-exhausted final failures; shutdown at the deadline with undelivered counts; per-destination isolation; concurrent enqueue and close under the race detector; the wire schema; HTTP status classification, redirects, slow and unavailable destinations, URL validation, and credential-free errors; event rendering, routing, and the app lifecycle; and the end-to-end flow from golemu over LLRP through tagstrak to an HTTP destination.
