# HTTP API — memory and PostgreSQL modes

Base URL: `http://127.0.0.1:8080`. Local trusted use; no authentication. Bodies are limited to 16 KiB. Unknown fields, invalid JSON and multiple JSON values are rejected. `X-Request-ID` is scoped to the API instance.

| Method / path | Success | Purpose |
| --- | --- | --- |
| `POST /v1/jobs` | 202 new / 200 replay | Submit with required `Idempotency-Key` |
| `GET /v1/jobs` | 200 | List recent PostgreSQL jobs with state filter and keyset cursor |
| `GET /v1/jobs/{id}` | 200 | Read snapshot |
| `GET /v1/jobs/{id}/events` | 200 | Read PostgreSQL lifecycle history with a forward cursor |
| `POST /v1/jobs/{id}/cancel` | 200 | Cancel queued/running/retrying; repeated cancel succeeds |
| `GET /healthz` | 200 | HTTP process responsive |
| `GET /readyz` | 200 / 503 | Admission enabled / draining |
| `GET /metrics` | 200 | Prometheus text exposition |

Readiness reflects lifecycle and, in PostgreSQL mode, database connectivity; it does not predict instantaneous queue occupancy or retention capacity.

## Job list

`GET /v1/jobs?state=failed&limit=50&cursor=CURSOR` is available in PostgreSQL
mode. Omit `state` to include all states; accepted filters are `queued`,
`running`, `retrying`, `succeeded`, `failed`, and `canceled`. `limit` defaults to
50 and accepts 1–100. Jobs are ordered newest first by `(created_at, id)`.

The opaque base64url cursor is exclusive and bound to the state filter. Pass it
back unchanged; changing or removing the filter requires starting a new page.
Keyset pagination prevents newer inserts from shifting later pages. It is not a
snapshot held across HTTP requests: retention may remove older rows between
pages. A final page returns `{"jobs":[]}` or a shorter list and omits
`next_cursor`.

List entries intentionally contain only `id`, `kind`, `state`, `attempts`,
`max_attempts`, `created_at`, `updated_at`, and an optional `error`. Fetch the
single-job resource for the full normalized spec or result. Memory mode returns
501 `job_list_unavailable` rather than presenting process-local semantics as the
durable operations view.

## Event history

`GET /v1/jobs/{id}/events?limit=50&cursor=EVENT_ID` is available in PostgreSQL
mode. `limit` defaults to 50 and accepts 1–100. Omit `cursor` for the first page;
subsequent requests pass the returned decimal `next_cursor`. Events are ordered
by increasing immutable ID, and the query reads at most `limit + 1` rows to
detect another page. An existing job beyond the final cursor returns
`{"events":[]}`; a missing or retained-away job returns 404.

The cursor is exclusive, so repeating the last event ID later can observe newly
appended transitions without duplicating older rows. Event IDs and cursors are
decimal strings, preserving 64-bit identity in JavaScript. Other fields are
`kind`, resulting `state`, `attempt`, `created_at`, and optional `lease_owner`,
`lease_version`, or `message`. Memory mode returns 501
`event_history_unavailable` because it does not retain this database history.

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
| 501 | `event_history_unavailable` | Start PostgreSQL mode to inspect durable lifecycle events |
| 501 | `job_list_unavailable` | Start PostgreSQL mode to list durable jobs |

The standard router supplies plain-text 404/405 for unmatched routes/methods; structured errors apply to application routes. Successful cancel marks state and signals context; it cannot undo external side effects. Cancel after success/failure returns 409; repeated cancellation returns 200.
