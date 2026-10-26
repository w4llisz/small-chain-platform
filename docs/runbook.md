# Local runbook

## Inspect

`make run` starts memory mode on loopback. `/healthz` checks process responsiveness; `/readyz` returns 503 when draining and PostgreSQL mode also checks database connectivity. JSON logs use `job_id` for transitions and `request_id` for HTTP requests. Payloads, keys and panic values are not logged; route templates replace arbitrary request paths.

| Signal | Interpretation |
| --- | --- |
| `small_chain_queue_depth` | Pending entries including canceled tombstones |
| `small_chain_job_state_metrics_up` | `1` when this scrape read state counts; `0` means state gauges are intentionally omitted |
| `small_chain_jobs{state="running"}` | Retained running jobs; PostgreSQL mode reads the database, not API-process memory |
| `small_chain_jobs{state="retrying"}` | Retained jobs waiting for another durable attempt; not an in-process backoff count in PostgreSQL mode |
| `small_chain_attempts_total`, `small_chain_retries_total` | Started attempts / scheduled retries (which can be canceled) |
| `small_chain_rejections_total` | Queue, retention and shutdown rejections |
| `small_chain_attempt_duration_seconds_sum/count` | Mean completed-attempt duration; no percentiles |
| `small_chain_worker_attempt_duration_seconds_bucket` | Process-local durable executor time in fixed buckets; excludes database calls |
| `small_chain_http_requests_total`, `small_chain_http_server_errors_total` | Completed routes including probes; scrape excludes itself until complete |

In memory mode, job-state gauges come from the engine's locked snapshot. In
PostgreSQL mode, one retention-bounded aggregate supplies all six states under a
one-second scrape budget. If it fails, alert on
`small_chain_job_state_metrics_up == 0`; do not interpret absent job-state
series as zero. The endpoint remains available so process-level evidence is not
lost during a database incident.

Process-local metrics reset on restart; state gauges count retained records.
Wait-time histograms, admission reasons and traces remain planned.

## Compose demo

`make compose-up` builds the scratch image and waits up to one minute for the
durable API, worker, Prometheus and Grafana to become healthy. PostgreSQL is
private to the Compose network. Every published HTTP port is restricted to host
loopback: API 8080, worker metrics 9091, Prometheus 9090 and Grafana 3000.
Inspect service state, scrape targets and the provisioned dashboard with:

```sh
docker compose ps
docker compose logs api worker migrate postgres prometheus grafana
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:9091/metrics
curl 'http://127.0.0.1:9090/api/v1/query?query=small_chain_job_state_metrics_up'
# Open http://127.0.0.1:3000/d/small-chain-overview
```

Use `make compose-down` to stop the stack while preserving its named database
and Prometheus volumes. Use `make compose-clean` when you intentionally want to
delete both kinds of local demo data. Grafana state is recreated from the files
under `deploy/observability`; anonymous Viewer access is enabled only for this
loopback demo. The local admin password in `compose.yaml` is for temporary UI
experimentation and edits disappear on restart.

`make compose-smoke` creates a separate project, verifies migration, readiness,
one real task, both raw metric endpoints, Prometheus samples and the dashboard
UID through Grafana's API, then always deletes its containers and volumes. If a
panel is empty, first inspect Prometheus **Status → Target health**, then query
`up{job=~"small-chain-(api|worker)"}`. Treat
`small_chain_job_state_metrics_up == 0` as a database collection failure. The
dashboard intentionally contains no production alert threshold or percentile.

## Bounded load experiment

Use `make load` for the default 100-request workload or pass explicit flags to
the command when collecting evidence:

```sh
go run ./cmd/small-chain-load \
  -requests=200 -concurrency=4 -key-prefix=m4-c4-run1 -delay-ms=25 \
  > load-result.json
```

The generator has no retry loop and exits after the configured request count.
Queue-full and record-capacity responses are valid measured outcomes; transport
or server errors are recorded rather than hidden. A reused key prefix produces
replays, so clean the Compose volumes or choose a new prefix before a fresh
admission run. See [the measurement protocol](measurement.md) before treating
any output as benchmark evidence.

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

Inspect one job's retained lifecycle in bounded pages:

```sh
curl 'http://127.0.0.1:8080/v1/jobs/JOB_ID/events?limit=50'
# Pass the returned next_cursor verbatim until it is omitted:
curl 'http://127.0.0.1:8080/v1/jobs/JOB_ID/events?limit=50&cursor=EVENT_ID'
```

The cursor is exclusive and can be reused later to poll for a newly appended
transition. An empty `events` array means the job exists but has no later event;
404 means the job is absent or has already been removed by retention. The
endpoint is intentionally unavailable in memory mode.

List recent durable jobs for operator triage:

```sh
curl 'http://127.0.0.1:8080/v1/jobs?state=failed&limit=50'
# Continue with the same state and the returned opaque cursor:
curl 'http://127.0.0.1:8080/v1/jobs?state=failed&limit=50&cursor=CURSOR'
```

Start a new page when changing the state filter. List summaries omit payloads
and results; use `GET /v1/jobs/{id}` for full detail.

## Troubleshooting

- **429:** inspect queue and retrying jobs. Reduce load; adjust workers using measured resource use. More queue capacity only permits more waiting.
- **503 record_capacity:** in memory mode, restart only if discarding demo history is acceptable. In PostgreSQL mode, inspect the shared cap/replay window and retention logs. Capacity remains full while records are active or inside the replay window; never delete active rows to make room.
- **Repeated retention failures:** check `maintenance.retention_failed`, database connectivity and permissions. Readiness reports the current database ping independently; fixing the cause lets the next cadence retry without restarting.
- **Stuck work:** inspect timeout/attempt/state. Executors must honor context; Go cannot preempt arbitrary task functions.
- **Missing HTTP records after restart:** expected only in default memory mode. Start with `-storage=postgres` and the same migrated `DATABASE_URL` for durable records.
- **Event history returns 501:** start the API with `-storage=postgres`; memory mode does not retain equivalent lifecycle evidence.
- **Job list returns 501:** start the API with `-storage=postgres`; the operations list intentionally represents durable shared state only.
- **Worker repeatedly logs claim/recovery failures:** verify `make migrate` completed and the worker role can access the migrated schema; do not grant migration DDL merely to hide startup errors.
- **Compose API or worker never starts:** inspect the one-shot `migrate` service first, then PostgreSQL health. Application services require a successful migration rather than retrying against an unknown schema.
- **Prometheus or Grafana never becomes healthy:** inspect producer health first, then `docker compose logs prometheus grafana`. Prometheus depends on both metric endpoints; Grafana depends on Prometheus. Provisioned dashboard edits must be made in the repository JSON rather than the UI.
- **Missing traces/percentiles:** planned, not present; the durable worker exports fixed histogram buckets but no measured percentile claim.

## CI

The workflow runs on PRs, main and feature-branch pushes, with read-only contents permission, pinned action commits and no secrets. It checks formatting, vet, race tests, a short fuzz run, static build, process smoke and container build. Dependabot tracks actions/Docker updates.

Workflow configuration alone does not prove Actions or Docker passed. Check the real run before merging. M2 provisions PostgreSQL and runs a race-instrumented worker process kill/recovery test; M3 adds type checking/Playwright; M4 adds reproducible load runs. Establish a baseline before gating on performance thresholds.

Authentication, authorization, transport security and resource/connection limits are prerequisites for public hosting, outside the local demonstration's M1 scope.
