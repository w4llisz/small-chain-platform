# Portfolio roadmap

## Positioning

**Small Chain: a Go job execution platform with explicit failure semantics.** Connect backend implementation to operating a service: concurrency bounds, overload, persistence, cancellation, observability, and an operator workflow.

Target investment: roughly 45–60 focused hours for M1–M4, adjusted by progress. These are planning estimates, not a promised calendar deadline. Stop adding features when acceptance criteria are met.

| Milestone | Scope / estimate | Acceptance criteria | Evidence |
| --- | --- | --- | --- |
| M1 — execution foundation (complete) | Go API, memory engine, bounded pool, retries, cancel, probes, metrics, tests/CI; 10–14 h | Concurrent same-key requests execute once; safe overload; immutable terminal results; signal drain/deadline cancellation | `make verify`, CI workflow, design decisions |
| M2 — durable scheduling (complete) | PostgreSQL migrations, admission, leases, fenced completion, delayed retries, events/retention; 14–18 h | Jobs survive restart; two processes claim safely; killed worker recovers; stale completion rejected; retry budget survives crashes | DB integration tests, recovery script, query plans |
| M3 — operator console (complete) | React/TypeScript, paginated/filterable list, details/events, cancel; 8–12 h | Find failures and causes; cancel queued/running tasks; loading/error states; Playwright workflow passes | 32 client/component tests and green real Go/PostgreSQL/Chromium CI run #43 |
| M4 — operational evidence (in progress) | Compose demo, durable metrics, OpenTelemetry, dashboard, reproducible load/failure tests; 12–16 h | One-command stack; trace admission/attempts; runbook matches failures; measurements include hardware/commit/workload | Database-backed gauges first; benchmark report and incident write-up later |

M1 is the runnable baseline, M2 is the completed durable platform core, and M3
is the completed operator workflow. **M4 is the current priority:** add evidence
that is generated from the real durable system without inventing production
scale, latency or reliability claims.

## Next PRs

1. **M2.1 implemented:** `internal/postgres` schema/migrations, unique idempotency keys, normalized request fingerprints, transactional submit/get and PostgreSQL CI. Tests cover 32 concurrent submissions, conflicting payloads, uncommitted winners committing/rolling back, reconnect/replay and migration rollback/history checks. HTTP/workers still use M1 memory; no durable execution claim. See [setup and contract](postgres.md).
   - **M2.2 implemented:** persisted attempt budgets, availability and lease owner/version/expiry; bounded batch `ClaimDue` uses `FOR UPDATE SKIP LOCKED`, database time and a partial ready index. Real-DB tests use two pools to claim 24 unique jobs concurrently, prove a locked head row does not block the next job, and exclude delayed/exhausted/running work. Claiming produces fencing tokens but does not yet execute or recover jobs.
   - **M2.3 implemented:** bounded heartbeat, success and failure/retry writes match the exact `(id, owner, lease_version, running, unexpired)` lease and use database time. Retry delay and lease release are one atomic update. Operator cancellation increments the version before clearing a lease. Real-DB tests prove stale/expired tokens cannot mutate state, retries exhaust the persisted budget, cancellation is idempotent, and a completion/cancel race has one terminal winner. The outcome migration also backfills rows created under the older state schema before enforcing result/error invariants.
   - **M2.4 implemented:** a bounded, single-statement `RecoverExpired` sweep selects expired running rows in lease order with `FOR UPDATE SKIP LOCKED`, moves them to delayed retry or failed according to the persisted attempt budget, advances the fencing version and clears the lease. A partial index targets only running leases. Real-DB tests prove two concurrent sweepers recover 24 jobs once each, live leases are ignored, retry budget survives recovery, and every stale completion is rejected.
   - **M2.5 implemented:** `internal/dbworker` polls only for available concurrency, executes the trusted handler after claim transactions commit, renews leases, runs bounded recovery sweeps, and persists success/retry/permanent failure with the current fencing token. Heartbeat loss cancels local execution without writing a late outcome. Shutdown stops the poll/recovery loop and drains only already claimed attempts. Unit tests cover the concurrency bound, panic isolation, retry classification, lease loss and drain order; the real-DB case proves a long attempt stays alive through heartbeat, a transient failure retries, and an expired claim recovers.
   - **M2.6 implemented:** `cmd/small-chain-worker` accepts a required unique owner and bounded lease/poll/recovery settings, reads its DSN only from `DATABASE_URL`, and drains on SIGTERM. A Linux process test starts two race-instrumented worker binaries against an isolated PostgreSQL schema, waits for worker A to claim a long attempt, kills A with SIGKILL, and proves worker B recovers and succeeds on attempt two. The recovery fence advances the lease version and rejects A's captured stale token.
   - **M2.7 implemented:** migration `0005_retention.sql` stores one shared admission policy and maintains its retained count with database triggers. Concurrent inserts cannot exceed the cap; an existing key still replays or conflicts at capacity. `PurgeTerminal` locks the policy, removes only terminal rows older than the minimum replay window in bounded `(updated_at, id)` batches, and leaves queued/running/retrying work untouched. Real-DB tests cover cap enforcement, replay at capacity, key reuse after cleanup, active-row safety and a 16-submit/cleanup race across two pools.
   - **M2.8 implemented:** `cmd/small-chain -storage=postgres` routes submit/get/cancel directly to the durable store and never creates the memory queue. HTTP 202 follows commit; replay/conflict/capacity retain their contract; readiness checks database connectivity. A race-instrumented real API binary test proves the record and key survive restart, then reads and cancels the same row.
   - **M2.9 implemented:** PostgreSQL API mode runs a bounded terminal purge at startup and on a configurable cadence. Sweeps are serial, failures are logged and retried without changing readiness, and shutdown cancels the loop before closing the pool. Race tests prove failure recovery and cancellation; the real API process case proves scheduled cleanup releases a one-row cap.
   - **M2.10 event foundation implemented:** migration `0006_job_events.sql` records one submission event and one event per claimed fencing version. Submission replay does not duplicate history; claim updates and event inserts share one data-modifying statement and transaction. A migration backfills only the admission timestamp known for older rows. Real-DB failure injection proves an event-write failure rolls back the job insert/capacity counter or claim/version update.
   - **M2.11 complete event coverage implemented:** migration `0007_outcome_events.sql` gives events a resulting state and bounded message, then records success, scheduled retry, worker failure, lease recovery and cancellation in the same statement as each fenced transition. Heartbeats are intentionally excluded. Database constraints prevent duplicate worker outcomes, recoveries and terminal events. Real-DB cases cover failure rollback, concurrent recovery and the completion/cancel race.
   - **M2.12 event reads implemented:** `ListEvents` uses one MVCC snapshot and the existing `(job_id, id)` index to return stable forward pages of at most 100 events. `GET /v1/jobs/{id}/events` validates decimal cursors and page bounds, distinguishes a missing job from an empty page, and is explicitly unavailable in memory mode. Real PostgreSQL and process-level HTTP tests cover page boundaries and submit/cancel history.
2. **M3.1 job-list API implemented:** migration `0008_job_list.sql` adds indexes for unfiltered and state-filtered recent-job scans. `GET /v1/jobs` returns lightweight summaries in newest-first `(created_at, id)` keyset pages of at most 100 rows. Opaque cursors are bound to the state filter; memory mode explicitly returns 501. Real PostgreSQL tests cover page boundaries, concurrent newer inserts, filtering, empty pages and index shape; the API process test covers cursor/filter behavior across restart.
   - **M3.2 read-only console implemented:** `web/` adds React/TypeScript, a runtime-checked list client, state filters, 20-row cursor pages and loading/empty/error/retry states. Nine client/component tests cover request encoding, malformed responses, filters, append, retry and stale-request isolation. CI adds an independent bounded frontend job. Browser visual QA and real-backend E2E are not yet verified.
   - **M3.3 read-only detail/history implemented:** selecting a job loads its normalized spec, state, attempts, result/error and retained lifecycle events through the existing Go endpoints. Snapshot and history start independently; event pages append oldest-first and preserve decimal string IDs/cursors beyond JavaScript's safe integer range. Loading, empty history, retained-away 404, independent retries and aborted stale responses have client/component coverage (20 frontend tests total). Browser visual QA and real-backend E2E remain unverified.
   - **M3.4 cancellation implemented:** the detail view offers a two-step confirmation only for queued/running/retrying snapshots. A pending request is guarded against duplicate clicks. After success, 409, or an uncertain failure, the console reloads snapshot, event history and the filtered list; it never optimistically declares canceled state or automatically repeats a write. Client/component tests cover confirmation dismissal, duplicate clicks, completion winning with 409, ambiguous network failure, terminal-state controls, selection changes and list refresh (32 frontend tests total). Browser visual QA and real-backend E2E remain unverified.
   - **M3.5 real-stack browser workflow implemented:** a single Chromium test runs against a migrated disposable PostgreSQL database, the compiled Go API, one Go worker and Vite. It seeds a five-second checksum job, waits for a real lease claim, filters/selects it, inspects the claimed event and owner, confirms cancellation, then reads the persisted canceled state through HTTP. Readiness waits, browser/test timeouts, process count and cleanup are bounded. CI installs Chromium and owns the PostgreSQL service; local unit/build checks do not pretend to execute this workflow without those dependencies.
   - **M3.6 complete:** real-stack CI run #43 passed its web, Go, PostgreSQL and Chromium jobs, including the persisted cancellation assertion. M3 is closed; subjective cross-browser visual review and a recorded demo remain optional evidence, not release blockers.
3. `feat/operations-console`: details/events, cancellation and Playwright workflow. Optional manual retry creates a new job/key with explicit lineage; never mutate a terminal record.
4. `feat/observability-and-evidence`: Compose with PostgreSQL/metrics/traces, dashboard, load generator and measured report.
   - **M4.1 durable state gauges implemented:** PostgreSQL mode aggregates the six retained lifecycle states from the database under a one-second scrape budget. A successful scrape publishes all states including zeroes. A failed read publishes `small_chain_job_state_metrics_up 0`, logs the failure and omits state gauges rather than serving stale or process-local values. Memory mode retains its in-process source. Real database and API-process tests cover the snapshot and exposition.
   - **Next small task — M4.2:** add bounded attempt-duration observations at the durable worker boundary with explicit buckets and tests; do not claim percentiles or production latency until a reproducible workload runs.

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
