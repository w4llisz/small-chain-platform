# Small Chain

A small Go task execution platform with explicit concurrency limits and failure semantics.

Submit a task through HTTP, observe its lifecycle, retry transient failures, and cancel work without corrupting the final state. The project explores the operational problems behind background jobs: overload, duplicate requests, deadlines, shutdown, and eventually worker crashes.

**Status: M1 memory mode and the M2 durable HTTP/worker path are runnable.** The default remains the bounded in-memory demo. With `-storage=postgres`, HTTP acceptance, lookup and cancellation use PostgreSQL as the only source of truth; a separate worker process claims the same durable records, while the API runs bounded terminal cleanup. M2.10 now records submission and claim events atomically; outcome events and their read API, tracing and a React console remain planned. This repository originally contained a blockchain placeholder; the name now refers to the task lifecycle. No blockchain node, consensus algorithm, or token is required.

## Try it in five minutes

Prerequisites: Go 1.27.x; `make` and Python 3 for full verification. The race detector requires a C compiler. The M1 server uses the Go standard library; the optional PostgreSQL store uses pgx, pinned in `go.mod`/`go.sum`.

```sh
make verify       # formatting, vet, race tests, build, real-process smoke test
make run          # listens on 127.0.0.1:8080
```

In another terminal:

```sh
curl -i -X POST http://127.0.0.1:8080/v1/jobs \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: demo-001' \
  -d '{"kind":"demo.checksum","payload":"hello","fail_first_attempts":2}'

# Replace JOB_ID with the returned id (or use the Location header).
curl http://127.0.0.1:8080/v1/jobs/JOB_ID
curl http://127.0.0.1:8080/metrics
```

Expected terminal result: `state=succeeded`, `attempts=3`, and SHA-256 `2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824`.
Repeat the same POST and key: HTTP 200 returns the same job. Change the payload under that key: HTTP 409.

```sh
# A long task gives you time to cancel it.
curl -s -X POST http://127.0.0.1:8080/v1/jobs \
  -H 'Idempotency-Key: cancel-001' \
  -d '{"kind":"demo.checksum","delay_ms":5000,"timeout_ms":10000}'
curl -X POST http://127.0.0.1:8080/v1/jobs/JOB_ID/cancel

# A deterministic timeout demo: fails after the first attempt.
curl -s -X POST http://127.0.0.1:8080/v1/jobs \
  -H 'Idempotency-Key: timeout-001' \
  -d '{"kind":"demo.checksum","delay_ms":500,"timeout_ms":20}'
```

`demo.checksum` hashes at most 4 KiB of text. Its delay and transient-failure fields are deliberate test controls. It does not execute shell commands, containers, arbitrary code, or outbound HTTP requests.

## What is implemented

| Capability | Contract / evidence |
| --- | --- |
| Bounded concurrency | Fixed worker pool; bounded pending channel; overload returns 429 with `Retry-After` |
| Atomic idempotency | One normalized spec per key for this process lifetime; concurrent submissions tested |
| Failure handling | Explicit retryable errors; capped exponential backoff with equal jitter; bounded attempts |
| Deadlines and cancellation | Per-attempt context; canceled jobs cannot be overwritten by late success |
| Graceful lifecycle | Stop admission, drain accepted tasks; cancel remaining work at shutdown deadline |
| Bounded retention | Cap on retained jobs and keys; new keys rejected at capacity; existing keys still replay |
| Durable HTTP contract | Optional PostgreSQL mode returns 202 only after commit; replay, conflict, capacity and restart behavior are process-tested |
| Retention maintenance | Startup and periodic bounded terminal cleanup; serial sweeps, retrying error logs and cancellation before pool close |
| Atomic lifecycle history | PostgreSQL submission and claim events commit or roll back with their job transition; replay does not duplicate history |
| Durable worker core | PostgreSQL admission cap, retention/replay policy, claims, heartbeat, fenced outcomes/cancel, recovery and bounded polling |
| Process crash recovery | Separate worker command; Linux SIGKILL test proves another process recovers and stale completion is fenced |
| Operational visibility | JSON logs, job IDs, generated request IDs, liveness/readiness, Prometheus text metrics |
| Verification | Unit and HTTP integration tests, race detector, fuzz target, real-binary SIGTERM smoke test, GitHub Actions |

## Architecture

```mermaid
flowchart TD
  Client["CLI / future React console"] --> API["Go HTTP API"]
  API --> Engine["Engine: admission and lifecycle"]
  Engine --> State["Bounded memory records and keys"]
  Engine --> Queue["Bounded pending queue"]
  Queue --> Workers["Fixed worker pool"]
  Workers --> Executor["Cooperative checksum executor"]
  Workers --> State
```

The mutex protects short state transitions and admission. Executors run outside it. Backoff occupies a worker slot in M1: a deliberate simplicity/fairness tradeoff described in the [architecture](docs/architecture.md). M2 replaces in-memory admission and scheduling with transactional PostgreSQL records and leases; it does not add Kafka or Redis.

## Repository map

| Path | Responsibility |
| --- | --- |
| `cmd/small-chain/` | Configuration, HTTP server, signals and shutdown |
| `cmd/small-chain-worker/` | PostgreSQL worker process, configuration, signals and graceful drain |
| `internal/jobs/` | Job model, state machine, concurrency, retries and tests |
| `internal/postgres/` | Migrations, durable HTTP admission, bounded retention, leases/outcomes/recovery and real-DB race tests |
| `internal/maintenance/` | Cancellable PostgreSQL retention cadence, structured failure reporting and race-tested retry behavior |
| `internal/dbworker/` | Bounded PostgreSQL poll/heartbeat/recovery loop with shutdown and fencing-loss tests |
| `cmd/migrate/` | Explicit schema migration command; see [PostgreSQL guide](docs/postgres.md) |
| `internal/httpapi/` | HTTP contract, structured logs, metrics and integration tests |
| `scripts/smoke.py` | Real binary: submit → retry → result; graceful and forced shutdown |
| `.github/workflows/ci.yml` | Formatting, vet, race, fuzz, build, smoke, container build and PostgreSQL integration |
| `docs/architecture.md` | Invariants, design tradeoffs, future PostgreSQL protocol |
| `docs/roadmap.md` | Scope, milestones, acceptance criteria and interview evidence |
| `docs/api.md` | Requests, responses, limits and error semantics |
| `docs/runbook.md` | Demo steps, troubleshooting and operational limitations |
| `docs/verification.md` | Observed local checks and verification limitations |

## Run and inspect

```sh
go run ./cmd/small-chain -workers=4 -queue=64 -max-jobs=10000 -shutdown-grace=10s
make fuzz

docker build -t small-chain:local .
docker run --rm -p 127.0.0.1:8080:8080 small-chain:local
```

The binary defaults to loopback. The container listens on all container interfaces; publish it only on local loopback for this unauthenticated demo. No external credentials are needed.

For durable mode, follow the [PostgreSQL setup and test guide](docs/postgres.md). Apply migrations explicitly, then run the API and at least one worker:

```sh
export DATABASE_URL='postgres://small_chain:local_demo_only@localhost:5432/small_chain_test?sslmode=disable'
make migrate
go run ./cmd/small-chain -storage=postgres -retention-interval=1m -retention-batch=100
# In another terminal, with the same DATABASE_URL:
go run ./cmd/small-chain-worker -owner=worker-a
```

`DATABASE_URL` stays in the environment rather than process arguments. The API
does not run DDL or start an in-process memory worker in this mode. Add more
workers only with unique owners.

## Limits and next milestones

- Restarting the default memory mode loses jobs, results and idempotency keys. Its HTTP 202 acknowledges memory admission. In PostgreSQL mode, 202 follows transaction commit and records survive API restart.
- PostgreSQL mode requires migrations and a reachable database before startup. Readiness fails if the database becomes unavailable; new submissions fail without falling back to memory.
- Recovery is at least once. Fencing protects the PostgreSQL row, not external side effects; exactly-once execution is not claimed.
- M1 memory records remain until process exit and reject new keys at `-max-jobs`. PostgreSQL defaults to 10,000 retained jobs and a 24-hour minimum replay window. The API deletes up to 100 eligible terminal rows at startup and every minute by default; `-retention-interval=0` disables this maintenance.
- Cancellation is cooperative. The built-in executor honors context; arbitrary Go functions cannot be forcibly stopped.
- No authentication, tenant isolation, public ingress, or production SLO is claimed.
- PostgreSQL mode currently exposes HTTP counters and a backend identity metric; its job/attempt gauges are not fabricated from process-local state. Durable metrics, percentile histograms, OpenTelemetry traces and load-test results are future work. No throughput claim has been measured yet.
- PostgreSQL currently records submission and claim history only. Outcome/recovery/cancel events and a paginated event endpoint remain planned; retained events are deleted with their job during bounded terminal cleanup.

The target portfolio release is a **Go + PostgreSQL job platform with a small React/TypeScript operations console**, backed by failure-recovery tests and reproducible measurements. See [milestones](docs/roadmap.md) and [design decisions](docs/architecture.md).

## License

Apache License 2.0; see [LICENSE](LICENSE).
