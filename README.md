# Small Chain

A small Go task execution platform with explicit concurrency limits and failure semantics.

Submit a task through HTTP, observe its lifecycle, retry transient failures, and cancel work without corrupting the final state. The project explores the operational problems behind background jobs: overload, duplicate requests, deadlines, shutdown, and eventually worker crashes.

**Status: M1 foundation.** The API and worker pool run in one process with bounded in-memory storage. PostgreSQL durability, multiple worker processes, tracing, and a React console are planned milestones, not implemented features. This repository originally contained a blockchain placeholder; the name now refers to the task lifecycle. No blockchain node, consensus algorithm, or token is required.

## Try it in five minutes

Prerequisites: Go 1.27.x; `make` and Python 3 for full verification. The race detector requires a C compiler. Runtime code uses only the Go standard library, so there is no `go.sum` yet.

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
| `internal/jobs/` | Job model, state machine, concurrency, retries and tests |
| `internal/httpapi/` | HTTP contract, structured logs, metrics and integration tests |
| `scripts/smoke.py` | Real binary: submit → retry → result; graceful and forced shutdown |
| `.github/workflows/ci.yml` | Formatting, vet, race, fuzz, build, smoke and container build |
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

## Limits and next milestones

- A restart loses all jobs, results and idempotency keys. HTTP 202 in M1 acknowledges memory admission, not durable storage.
- One process only. There is no distributed scheduling, lease renewal, crash recovery, or exactly-once guarantee.
- Completed records are retained until the process exits. At `-max-jobs`, new submissions receive 503. M2 adds an explicit retention policy.
- Cancellation is cooperative. The built-in executor honors context; arbitrary Go functions cannot be forcibly stopped.
- No authentication, tenant isolation, public ingress, or production SLO is claimed.
- Metrics expose counters and an attempt-duration sum/count. Percentile histograms, OpenTelemetry traces and load-test results are future work. No throughput claim has been measured yet.

The target portfolio release is a **Go + PostgreSQL job platform with a small React/TypeScript operations console**, backed by failure-recovery tests and reproducible measurements. See [milestones](docs/roadmap.md) and [design decisions](docs/architecture.md).

## License

Apache License 2.0; see [LICENSE](LICENSE).
