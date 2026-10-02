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
   - **Next small task — M2.3:** heartbeat and fenced completion/retry/cancel writes. Acceptance: every mutation checks `(id, owner, lease_version, running)`; stale tokens update zero rows; retry persists `available_at` and clears lease fields atomically.
   - **Later M2 steps:** expired-lease crash recovery with persisted retry limits; HTTP/worker integration; bounded admission and explicit retention/replay policy. Each step needs real-database tests before being marked complete.
2. `feat/leased-workers`: claim, heartbeat, fenced finish/cancel, durable backoff, worker modes and two-process crash tests.
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
6. After M2, kill a worker and explain lease recovery/fencing.

## Resume wording

After M1: “Built a Go background-job service with bounded worker concurrency, idempotent admission, retry/cancellation semantics, structured logs, metrics and race-tested lifecycle handling.”

Add PostgreSQL recovery, React, tracing and performance numbers only after implementation and measurement. Present simulated workloads as a portfolio demonstration, not production usage.
