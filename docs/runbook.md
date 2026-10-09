# Local runbook

## Inspect

`make run` starts memory mode on loopback. `/healthz` checks process responsiveness; `/readyz` returns 503 when draining and PostgreSQL mode also checks database connectivity. JSON logs use `job_id` for transitions and `request_id` for HTTP requests. Payloads, keys and panic values are not logged; route templates replace arbitrary request paths.

| Signal | Interpretation |
| --- | --- |
| `small_chain_queue_depth` | Pending entries including canceled tombstones |
| `small_chain_jobs{state="running"}` | Running state; not necessarily live goroutines after cooperative cancel |
| `small_chain_jobs{state="retrying"}` | Worker slots occupied by backoff |
| `small_chain_attempts_total`, `small_chain_retries_total` | Started attempts / scheduled retries (which can be canceled) |
| `small_chain_rejections_total` | Queue, retention and shutdown rejections |
| `small_chain_attempt_duration_seconds_sum/count` | Mean completed-attempt duration; no percentiles |
| `small_chain_http_requests_total`, `small_chain_http_server_errors_total` | Completed routes including probes; scrape excludes itself until complete |

Metrics reset on restart; state gauges count retained records. M4 adds wait-time histograms, admission reasons and traces.

## Failure demos

| Scenario | Reproduction | Expected |
| --- | --- | --- |
| Transient errors | `fail_first_attempts=2, max_attempts=3` | Success on attempt 3 |
| Exhaustion | `fail_first_attempts=3, max_attempts=3` | Failure on attempt 3 |
| Timeout | `delay_ms=500, timeout_ms=20` | Failure on first attempt |
| Queue saturation | `-workers=1 -queue=1`; rapidly submit 3 keys with `delay_ms=5000, timeout_ms=10000` | Running + queued + 429 |
| Retention cap | `-max-jobs=1`; complete one task, submit another key | 503; first key still replays |
| Graceful shutdown | SIGTERM with short accepted tasks | Drain; exit 0 |
| Forced shutdown | SIGTERM with tasks longer than shutdown grace | Cancel remaining work; nonzero exit |

`make smoke` automates results/retry/idempotency and both shutdown paths against the real binary. `go test -race ./...` exercises parallel admission and transitions.

## Durable worker processes

Apply migrations once with the schema-owner credentials, then start each worker
with a unique owner:

```sh
export DATABASE_URL='postgres://small_chain:local_demo_only@localhost:5432/small_chain_test?sslmode=disable'
make migrate
go run ./cmd/small-chain -storage=postgres -retention-interval=1m -retention-batch=100
# In another terminal:
go run ./cmd/small-chain-worker -owner=worker-a
```

The worker does not run DDL and never prints the DSN. SIGTERM stops new claims
and drains accepted attempts. SIGKILL leaves the current lease for another
worker's recovery sweep. `make test-integration` automates the two-process kill
case and the durable HTTP restart case against disposable schemas.

The durable store defaults to 10,000 retained rows and a 24-hour minimum replay
window. Replays continue after that window until a purge succeeds. The API runs
one bounded purge at startup and every minute by default; change
`-retention-interval` and `-retention-batch`, or use interval zero to disable it.
Only terminal jobs are eligible, and workers never run cleanup. Successful
nonempty sweeps log `maintenance.retention_purged`; failures log
`maintenance.retention_failed` and retry on the next cadence without flapping
readiness. The memory-only `-max-jobs` flag does not configure the shared
PostgreSQL policy.

## Troubleshooting

- **429:** inspect queue and retrying jobs. Reduce load; adjust workers using measured resource use. More queue capacity only permits more waiting.
- **503 record_capacity:** in memory mode, restart only if discarding demo history is acceptable. In PostgreSQL mode, inspect the shared cap/replay window and retention logs. Capacity remains full while records are active or inside the replay window; never delete active rows to make room.
- **Repeated retention failures:** check `maintenance.retention_failed`, database connectivity and permissions. Readiness reports the current database ping independently; fixing the cause lets the next cadence retry without restarting.
- **Stuck work:** inspect timeout/attempt/state. Executors must honor context; Go cannot preempt arbitrary task functions.
- **Missing HTTP records after restart:** expected only in default memory mode. Start with `-storage=postgres` and the same migrated `DATABASE_URL` for durable records.
- **Worker repeatedly logs claim/recovery failures:** verify `make migrate` completed and the worker role can access the migrated schema; do not grant migration DDL merely to hide startup errors.
- **Missing traces/percentiles:** planned, not present; duration is sum/count only.

## CI

The workflow runs on PRs, main and feature-branch pushes, with read-only contents permission, pinned action commits and no secrets. It checks formatting, vet, race tests, a short fuzz run, static build, process smoke and container build. Dependabot tracks actions/Docker updates.

Workflow configuration alone does not prove Actions or Docker passed. Check the real run before merging. M2 provisions PostgreSQL and runs a race-instrumented worker process kill/recovery test; M3 adds type checking/Playwright; M4 adds reproducible load runs. Establish a baseline before gating on performance thresholds.

Authentication, authorization, transport security and resource/connection limits are prerequisites for public hosting, outside the local demonstration's M1 scope.
