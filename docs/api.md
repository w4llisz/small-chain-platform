# HTTP API — memory and PostgreSQL modes

Base URL: `http://127.0.0.1:8080`. Local trusted use; no authentication. Bodies are limited to 16 KiB. Unknown fields, invalid JSON and multiple JSON values are rejected. `X-Request-ID` is scoped to the API instance.

| Method / path | Success | Purpose |
| --- | --- | --- |
| `POST /v1/jobs` | 202 new / 200 replay | Submit with required `Idempotency-Key` |
| `GET /v1/jobs/{id}` | 200 | Read snapshot |
| `POST /v1/jobs/{id}/cancel` | 200 | Cancel queued/running/retrying; repeated cancel succeeds |
| `GET /healthz` | 200 | HTTP process responsive |
| `GET /readyz` | 200 / 503 | Admission enabled / draining |
| `GET /metrics` | 200 | Prometheus text exposition |

Readiness reflects lifecycle and, in PostgreSQL mode, database connectivity; it does not predict instantaneous queue occupancy or retention capacity. There is no job-list or job-event endpoint yet; retain returned IDs. M3 adds cursor pagination and filters.

## Submit

`Idempotency-Key`: 1–128 ASCII letters/digits or `. _ : -`. Its scope is one process lifetime in default memory mode and the configured database retention lifetime in PostgreSQL mode. Omitted and explicit defaults compare equally; every normalized spec field participates. Rejected submissions consume no key. Responses include `Location` and `Idempotency-Replayed`.

```json
{"kind":"demo.checksum","payload":"hello","delay_ms":0,"fail_first_attempts":2,"max_attempts":3,"timeout_ms":1000}
```

| Field | Default | Accepted values |
| --- | --- | --- |
| `kind` | required | `demo.checksum` |
| `payload` | empty | Up to 4096 UTF-8 bytes |
| `delay_ms` | 0 | 0–5000 per attempt, demo control |
| `fail_first_attempts` | 0 | 0–5, demo transient failures |
| `max_attempts` | 3 | 1–5 including first attempt; explicit 0 selects default |
| `timeout_ms` | 1000 | 1–30000 per attempt; explicit 0 selects default |

Queue wait/backoff do not consume the attempt timeout. No whole-job deadline is exposed. Disconnecting HTTP after acceptance does not cancel a job; use the cancel endpoint. In PostgreSQL mode a new 202 is sent only after the submit transaction commits. An ambiguous transport failure should be retried with the same key.

A job contains `id`, normalized `spec`, `state`, `attempts`, `created_at`, `updated_at` and optional `result`/`error`. Timestamps are RFC 3339 UTC. `result` is lowercase SHA-256 hex. `spec` is returned to callers; do not put secrets in demo payloads.

## Errors

```json
{"error":{"code":"queue_full","message":"queue is full"}}
```

| HTTP | Code | Client behavior |
| --- | --- | --- |
| 400 | `invalid_json`, `invalid_request` | Correct body/key |
| 404 | `not_found` | Check ID; only memory mode erases records on restart |
| 409 | `conflict` | Different request for key, or cancel after success/failure |
| 413 | `body_too_large` | Reduce body |
| 429 | `queue_full` | Same-key retry with bounded client backoff; `Retry-After: 1` |
| 503 | `draining` | Stop submitting to this process |
| 503 | `record_capacity` | Memory: full until restart. PostgreSQL: inspect the cap, replay window and scheduled-retention logs |
| 500 | `internal_error` | Inspect logs; retry using same key |

The standard router supplies plain-text 404/405 for unmatched routes/methods; structured errors apply to application routes. Successful cancel marks state and signals context; it cannot undo external side effects. Cancel after success/failure returns 409; repeated cancellation returns 200.
