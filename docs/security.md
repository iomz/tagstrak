# Threat model

This is the v2 threat model required by #10.
It covers tagstrak and golemu as deployed from this repository; the decisions behind it are in the #9 and #10 design records.

## Trust boundaries

| Boundary | Trust | Controls |
| --- | --- | --- |
| LLRP reader network | Untrusted bytes | Codec frame, parameter, and count limits; per-frame deadlines; bounded queues; reconnect budget ([runtime.md](runtime.md)) |
| Management API | Same local user (Unix socket) or token holder (loopback TCP) | Off by default; loopback only; constant-time bearer token; request, header, and connection limits; server deadlines; audit ([control.md](control.md)) |
| Telemetry listener | Read-only, unauthenticated | Loopback by default; no mutation routes; connection cap and deadlines |
| Report destinations | Outbound, partially trusted | TLS verified against system roots; no redirects; URLs never in logs, errors, or metrics; bounded retries ([report.md](report.md)) |
| Configuration, state, and secret files | Local operator | Strict decoding; size limits; no inline secrets; private secret files read through one checked handle; atomic private state writes ([configuration.md](configuration.md)) |
| Environment | Local operator | Scalar settings and named secrets only |

## Abuse cases

| Abuse | Control |
| --- | --- |
| Unauthenticated mutation | Control off by default; TCP always authenticated; 401 before routing |
| Token guessing | At least 32-byte tokens; constant-time comparison; connection cap; every failure audited |
| Remote exposure | Non-loopback control addresses refused at startup |
| Browser-originated requests to loopback | Bearer token required on TCP; JSON content type required for mutations |
| Oversized bodies or headers | Per-route body limits; 8 KiB headers |
| Slow clients and connection exhaustion | Read-header, read, write, idle deadlines; connections beyond the cap closed immediately |
| Subscription and pattern floods | 10,000 subscriptions, 64 patterns each, 1 MiB documents |
| SSRF through the API | Destinations fixed at startup; the API cannot set URLs or headers |
| Credential leaks | Redacted `Secret` type; header-only credentials; URLs and tokens never logged, audited, or returned |
| Spoofed client addresses | Forwarded headers ignored; actor is the transport peer |
| Header injection through secrets | Header names must be tokens; control characters rejected at startup |
| Secret file swap | Secret files validated and read through the same handle |
| Partial or torn state | Validate, write atomically, then publish; failed writes change nothing |
| Hostile reader input | Bounded LLRP decoding; malformed frames end the session |

## Residual risks

- Authentication failures are not rate-limited beyond the connection cap and deadlines.
- The Unix socket trusts every process running as the same user.
- Secret rotation requires a restart.
- Remote management depends on an external tunnel or proxy for TLS.
- Telemetry exposes destination names and counters to anyone who can reach its listener.
- Report delivery is at-least-once in-process; events are lost on crash ([report.md](report.md)).
