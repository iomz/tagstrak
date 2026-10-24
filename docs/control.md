# Management API

Issue #10 slice 10b adds an authenticated management API to gosstrak and moves golemu's local control onto the same transport.
The [#10 design record](https://github.com/iomz/tagstrak/issues/10#issuecomment-6378251881) is the decision source; [security.md](security.md) holds the threat model.

## Transport

`internal/control` serves both commands and owns only transport concerns; each command supplies its routes.

| Bound | Value |
| --- | --- |
| Read-header / read / write / idle timeout | 2 s / 5 s / 5 s / 30 s |
| Shutdown | the command's shutdown timeout |
| Header bytes | 8 KiB |
| Request body | 1 MiB for gosstrak subscription documents; golemu's scenario limit for scenarios |
| Concurrent connections | 16 for gosstrak, 4 for golemu; excess connections are closed, not queued |
| Mutating requests | `Content-Type: application/json`, otherwise 415 |

Control is off unless configured, so default startup exposes no mutation endpoint.

- **Unix socket** (`--control-socket` or `[control] socket`): absolute path, private parent directory (0700), created with mode 0600, never replacing an existing path, removed on shutdown. It trusts the same local user and needs no token.
- **Loopback TCP** (`--control-address` or `[control] address`): `127.0.0.0/8`, `::1`, or `localhost` only, and always with a bearer token of at least 32 bytes from `[control] token`. Loopback is reachable by every local user and process and by browsers through DNS rebinding, so it is never trusted on its own.
- **Non-loopback addresses are refused.** TLS terminates outside the process: use an SSH tunnel, a reverse proxy on the same host, `kubectl port-forward`, or a sidecar, each reaching loopback with the token.

Authentication is `Authorization: Bearer <token>`, compared in constant time. A missing or wrong token gets 401 with a fixed body before routing, and the request body is never read.
`Forwarded` and `X-Forwarded-*` headers are ignored; the recorded actor is `unix` or the TCP peer address.
Every response carries `X-Request-ID` and `Cache-Control: no-store`.
Errors are structured and never echo headers or secrets:

```json
{"error": {"code": "invalid_subscriptions", "message": "…", "field": "subscriptions[0].patterns[0]"}}
```

## gosstrak routes

| Route | Behaviour |
| --- | --- |
| `GET /v1/status` | The telemetry status document |
| `GET /v1/subscriptions` | `{"source", "revision", "subscriptions"}`; the revision is also the `ETag` |
| `PUT /v1/subscriptions` | Replace the whole set and its routes; `"api"` source only |

`PUT` takes `{"subscriptions": [...]}` in the configuration file's subscription schema and honours `If-Match: "<revision>"`.
The field is required: `[]` removes every subscription, while a missing or `null` field is rejected so a malformed request cannot clear the set.

| Status | Code | Meaning |
| --- | --- | --- |
| 200 | | Replaced; the body and `ETag` carry the new revision |
| 400 | `invalid_json`, `invalid_subscriptions`, `invalid_precondition` | Malformed body, invalid subscription (with `field`), or malformed `If-Match` |
| 409 | `managed_by_file` | The configuration file owns subscriptions; edit it and send `SIGHUP` |
| 412 | `revision_mismatch` | `If-Match` is not the current revision |
| 413 | `body_too_large`, `state_too_large` | Body over 1 MiB, or subscriptions that would make the indented state file exceed 1 MiB; nothing changed |
| 415 | `unsupported_media_type` | Not `application/json` |
| 500 | `state_not_saved` | The state file could not be written; nothing changed |

Replacement validates and compiles the whole set, writes the state file, and only then publishes the new subscriptions and routes as one snapshot, so a tag never matches new subscriptions with old routes, and a failed write changes nothing.
Destinations are fixed at startup: the API cannot add destinations, change URLs or headers, or read credentials.

```sh
curl --unix-socket /run/gosstrak/control.sock http://localhost/v1/subscriptions

curl -H "Authorization: Bearer $GOSSTRAK_CONTROL_TOKEN" \
  -H 'Content-Type: application/json' -H 'If-Match: "3"' \
  -X PUT --data '{"subscriptions":[{"id":"product","patterns":["urn:epc:idpat:sgtin:0614141.812345.*"],"destinations":["erp"]}]}' \
  http://127.0.0.1:9090/v1/subscriptions
```

## Subscription sources

Exactly one source holds subscriptions, chosen with `[subscription_store] source` ([configuration.md](configuration.md)):

- **`file`** (default): the configuration file. `SIGHUP` re-reads it and replaces the subscriptions atomically; destinations, secrets, and other settings change only on restart. An invalid file or a source change is rejected and the current subscriptions stay. `PUT` returns 409.
- **`api`**: a state file gosstrak owns at `state_file`, written atomically (temporary file, sync, rename, directory sync) with mode 0600. Startup loads it, or starts empty when it does not exist; a damaged state file stops startup. The configuration file must not list subscriptions.

Writing API changes back into the configuration file is not supported: it would destroy comments, race with operators, and fight configuration management.

## Audit

Every mutation, reload, and authentication failure emits one `slog` record at info level:

| Field | Content |
| --- | --- |
| `event` | `control` |
| `action` | `subscriptions.replace`, `subscriptions.reload`, `scenario.replace`, `auth` |
| `outcome` | `ok`, `rejected`, `conflict`, `unauthenticated` |
| `actor` | `unix`, `signal`, or the TCP peer address |
| `request_id` | per request, also in `X-Request-ID` |
| `revision_before`, `revision_after`, `count`, `error_code` | where they apply |

Payloads, patterns, headers, and tokens are never recorded.

## golemu

golemu keeps `GET /status` and `PUT /scenario` on its Unix socket only; it is a test tool with no remote use case.
`PUT /scenario` now requires `Content-Type: application/json`, returns structured errors, and audits each replacement with the scenario revision and cycle count.

## Verification

```sh
go test -race ./internal/control/... ./internal/app/... ./cmd/...
```

Tests cover listener rules for sockets and loopback, refusal of non-loopback and hostname addresses, token strength, fail-closed authentication that never reaches handlers or reads bodies, ignored forwarded headers, the connection cap, server deadlines, strict JSON bodies, audit shapes without secrets or payloads, every API status, persistence before publication, failed state writes, `If-Match`, a live loopback listener with and without the token, `SIGHUP` reload with a real signal, and golemu's content-type check.
