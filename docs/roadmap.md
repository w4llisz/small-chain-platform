# Portfolio roadmap

## Positioning

**Small Chain: a Go job execution platform with explicit failure semantics.** Connect backend implementation to operating a service: concurrency bounds, overload, persistence, cancellation, observability, and an operator workflow.

Target investment: roughly 45–60 focused hours for M1–M4, adjusted by progress. These are planning estimates, not a promised calendar deadline. Stop adding features when acceptance criteria are met.

| Milestone | Scope / estimate | Acceptance criteria | Evidence |
| --- | --- | --- | --- |
| M1 — execution foundation (complete) | Go API, memory engine, bounded pool, retries, cancel, probes, metrics, tests/CI; 10–14 h | Concurrent same-key requests execute once; safe overload; immutable terminal results; signal drain/deadline cancellation | `make verify`, CI workflow, design decisions |
| M2 — durable scheduling (in progress) | PostgreSQL migrations, admission, leases, fenced completion, delayed retries, events/retention; 14–18 h | Jobs survive restart; two processes claim safely; killed worker recovers; stale completion rejected; retry budget survives crashes | DB integration tests, recovery script, query plans |
| M3 — operator console | React/TypeScript, paginated/filterable list, details/events, cancel; 8–12 h | Find failures and causes; cancel queued/running tasks; loading/error states; Playwright workflow passes | Recorded demo, screenshots, E2E tests |
| M4 — operational evidence | Compose demo, histograms, OpenTelemetry, dashboard, reproducible load/failure tests; 12–16 h | One-command stack; trace admission/attempts; runbook matches failures; measurements include hardware/commit/workload | Benchmark report with raw data; incident write-up |

M1 is a runnable baseline, not yet the full flagship portfolio. **M2 is the next priority:** it turns a worker-pool exercise into a durable platform project. Build M3 after persistence stabilizes.

## Next PRs

1. **M2.1 implemented:** `internal/postgres` schema/migrations, unique idempotency keys, normalized request fingerprints, transactional submit/get and PostgreSQL CI. Tests cover 32 concurrent submissions, conflicting payloads, uncommitted winners committing/rolling back, reconnect/replay and migration rollback/history checks. HTTP/workers still use M1 memory; no durable execution claim. See [setup and contract](postgres.md).
   - **M2.2 implemented:** persisted attempt budgets, availability and lease owner/version/expiry; bounded batch `ClaimDue` uses `FOR UPDATE SKIP LOCKED`, database time and a partial ready index. Real-DB tests use two pools to claim 24 unique jobs concurrently, prove a locked head row does not block the next job, and exclude delayed/exhausted/running work. Claiming produces fencing tokens but does not yet execute or recover jobs.
   - **M2.3 implemented:** bounded heartbeat, success and failure/retry writes match the exact `(id, owner, lease_version, running, unexpired)` lease and use database time. Retry delay and lease release are one atomic update. Operator cancellation increments the version before clearing a lease. Real-DB tests prove stale/expired tokens cannot mutate state, retries exhaust the persisted budget, cancellation is idempotent, and a completion/cancel race has one terminal winner. The outcome migration also backfills rows created under the older state schema before enforcing result/error invariants.
   - **M2.4 implemented:** a bounded, single-statement `RecoverExpired` sweep selects expired running rows in lease order with `FOR UPDATE SKIP LOCKED`, moves them to delayed retry or failed according to the persisted attempt budget, advances the fencing version and clears the lease. A partial index targets only running leases. Real-DB tests prove two concurrent sweepers recover 24 jobs once each, live leases are ignored, retry budget survives recovery, and every stale completion is rejected.
   - **M2.5 implemented:** `internal/dbworker` polls only for available concurrency, executes the trusted handler after claim transactions commit, renews leases, runs bounded recovery sweeps, and persists success/retry/permanent failure with the current fencing token. Heartbeat loss cancels local execution without writing a late outcome. Shutdown stops the poll/recovery loop and drains only already claimed attempts. Unit tests cover the concurrency bound, panic isolation, retry classification, lease loss and drain order; the real-DB case proves a long attempt stays alive through heartbeat, a transient failure retries, and an expired claim recovers.
   - **M2.6 implemented:** `cmd/small-chain-worker` accepts a required unique owner and bounded lease/poll/recovery settings, reads its DSN only from `DATABASE_URL`, and drains on SIGTERM. A Linux process test starts two race-instrumented worker binaries against an isolated PostgreSQL schema, waits for worker A to claim a long attempt, kills A with SIGKILL, and proves worker B recovers and succeeds on attempt two. The recovery fence advances the lease version and rejects A's captured stale token.
   - **Next small task — M2.7:** define and implement bounded durable admission and retention/replay policy before exposing PostgreSQL submission through HTTP. Acceptance: capacity is enforced transactionally, active work cannot be removed, replay behavior has a documented window, and concurrent submit/cleanup races have real-DB tests.
   - **Later M2 steps:** database-backed HTTP admission and append-only events. Each step needs real-database tests before being marked complete.
2. `feat/durable-admission-policy`: bound PostgreSQL retention and idempotency replay before the HTTP API acknowledges durable work.
3. `feat/operations-console`: list/filter API, React and Playwright. Optional manual retry creates a new job/key with explicit lineage; never mutate a terminal record.
4. `feat/observability-and-evidence`: Compose with PostgreSQL/metrics/traces, dashboard, load generator and measured report.

## Build on existing experience

| Strength | Extension in this project | Interview question answered by code |
| --- | --- | --- |
| React / TypeScript | Console for a real async backend | How do you prevent stale UI status or duplicate actions? |
| SQL | Transactional claims, uniqueness, indexes, migrations | What happens when two workers select the same row? |
| Reliability / infrastructure | Backpressure, shutdown, recovery, runbook | What survives a process kill? What does HTTP 202 guarantee? |
| E2E testing | Real HTTP/process tests; later Playwright + DB | How do tests prove recovery beyond the happy path? |
| Go transition | Context ownership, goroutine bounds, synchronization | Who closes the queue? Can cancellation race with completion? |

## Measurement plan (not results)

Compare 1/4/16 workers and bounded queues with specified payloads/delays/failure rates. Record offered load, acceptance/rejection, completion rate, API latency and queue-wait distributions, execution duration, RSS and goroutines. Include warm-up, duration, toolchain, CPU/memory, commit and raw output. Separate checksum overhead from simulated I/O workloads.

Overload should show bounded queues/records and admission rejection. Worker-kill recovery should show no permanently stranded jobs, bounded duplicate attempts, and rejected stale completion. Report limitations, including M1 worker-held backoff. Do not infer throughput from worker count or invent p95/SLO results.

## Five-minute interview demo

1. Explain the lifecycle and acceptance contract.
2. Submit, replay the key, then conflict the payload.
3. Inject two transient failures; inspect attempts, logs and metrics.
4. Saturate a small queue; demonstrate rejection and recovery.
5. Cancel and shut down; point to tests proving terminal immutability.
6. Run the process integration case; explain why SIGKILL creates a duplicate attempt but the stale token cannot corrupt state.

## Resume wording

After M1: “Built a Go background-job service with bounded worker concurrency, idempotent admission, retry/cancellation semantics, structured logs, metrics and race-tested lifecycle handling.”

Add PostgreSQL recovery, React, tracing and performance numbers only after implementation and measurement. Present simulated workloads as a portfolio demonstration, not production usage.
