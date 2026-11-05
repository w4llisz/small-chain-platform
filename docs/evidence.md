# Portfolio evidence index

This page is the shortest review path through Small Chain. Each claim points to
implementation, an executable check and retained runtime evidence where one
exists. The final column states the boundary of the claim so an interview demo
does not present a synthetic workload as production experience.

## Claim-to-evidence map

| Implemented claim | Core implementation | Strongest executable check | Reviewed runtime evidence | Limit / non-claim |
| --- | --- | --- | --- | --- |
| Admission and execution stay bounded under concurrency | [`internal/jobs/engine.go`](../internal/jobs/engine.go), [`internal/httpapi/api.go`](../internal/httpapi/api.go) | [`TestBoundedWorkersAndAdmission` and concurrent shutdown tests](../internal/jobs/engine_test.go); race-enabled `make verify` | [`CI #58` verify job](https://github.com/w4llisz/small-chain-platform/actions/runs/41818785830) | Bounds the worker pool, pending queue, retained records and request bodies; it is not a global HTTP connection limit. |
| Same-key submissions are atomic and durable acceptance survives API restart | [`internal/postgres/store.go`](../internal/postgres/store.go), [`0001_admission.sql`](../internal/postgres/migrations/0001_admission.sql), [`0005_retention.sql`](../internal/postgres/migrations/0005_retention.sql) | [`TestConcurrentIdempotency`, `TestUncommittedWinner` and persistence cases](../internal/postgres/store_integration_test.go); [real API restart test](../internal/httpapi/postgres_process_integration_test.go) | [`CI #58` PostgreSQL job](https://github.com/w4llisz/small-chain-platform/actions/runs/41818785830) | HTTP 202 means the job row committed. Replay is guaranteed only while retention keeps the key. Database disaster recovery is outside this demo. |
| Multiple workers claim safely and stale workers cannot overwrite recovered state | [`claim.go`](../internal/postgres/claim.go), [`lifecycle.go`](../internal/postgres/lifecycle.go), [`recovery.go`](../internal/postgres/recovery.go), [`dbworker/worker.go`](../internal/dbworker/worker.go) | [`SKIP LOCKED` claim tests](../internal/postgres/claim_integration_test.go), [fenced lifecycle tests](../internal/postgres/lifecycle_integration_test.go), [process SIGKILL test](../internal/dbworker/process_integration_test.go) | [Reviewed worker-crash bundle](incidents/README.md) from [`CI #57`](https://github.com/w4llisz/small-chain-platform/actions/runs/41617447374) | Execution is at least once. Fencing protects PostgreSQL state, not an external side effect; integrations still need idempotency. |
| Operators can inspect durable state/history and cancel work without optimistic UI state | [`event_read.go`](../internal/postgres/event_read.go), [`job_list.go`](../internal/postgres/job_list.go), [`web/src/App.tsx`](../web/src/App.tsx), [`Cancellation.tsx`](../web/src/Cancellation.tsx) | [Cursor/history database tests](../internal/postgres/events_integration_test.go), [React cancellation tests](../web/src/Cancellation.test.tsx), [real browser workflow](../web/e2e/operator.spec.ts) | [`CI #58` Web and E2E jobs](https://github.com/w4llisz/small-chain-platform/actions/runs/41818785830) | Cancellation is cooperative and can lose to completion. Event history is retained with the job; list pagination is not a cross-request snapshot. |
| Shutdown and retention have explicit ownership and bounded work | [`engine.go`](../internal/jobs/engine.go), [`maintenance/retention.go`](../internal/maintenance/retention.go), [`postgres/retention.go`](../internal/postgres/retention.go) | [drain/forced-shutdown race test](../internal/jobs/engine_test.go), [maintenance cancellation tests](../internal/maintenance/retention_test.go), [retention integration tests](../internal/postgres/retention_integration_test.go) | [`CI #58` verify and PostgreSQL jobs](https://github.com/w4llisz/small-chain-platform/actions/runs/41818785830) | Forced shutdown can cancel unfinished cooperative work. The replay window is a minimum, not an exact deletion time. |
| Metrics and optional traces expose failures without joining observability to readiness | [`httpapi/backend.go`](../internal/httpapi/backend.go), [`dbworker/metrics.go`](../internal/dbworker/metrics.go), [`telemetry/tracing.go`](../internal/telemetry/tracing.go), [`compose.yaml`](../compose.yaml) | [state-read failure test](../internal/httpapi/api_test.go), [fixed-bucket metric test](../internal/dbworker/metrics_test.go), [bounded tracing tests](../internal/telemetry/tracing_test.go), trace Compose smoke in CI | [`CI #58` verify job](https://github.com/w4llisz/small-chain-platform/actions/runs/41818785830) | Worker metrics reset on restart. Traces correlate processes by job ID but are separate roots. No production alert threshold or SLO is claimed. |
| Controlled load evidence is commit-bound, semantically validated and reviewable | [`internal/loadgen`](../internal/loadgen), [`internal/evidence`](../internal/evidence), [`scripts/measure-compose.sh`](../scripts/measure-compose.sh) | [load generator tests](../internal/loadgen/loadgen_test.go), [tamper/provenance tests](../internal/evidence/evidence_test.go), deterministic report tests | [Reviewed 540-job measurement bundle](measurements/README.md) from [`CI #53`](https://github.com/w4llisz/small-chain-platform/actions/runs/41043908030) | One fixed-order run on a shared CI runner does not establish throughput, percentiles, scaling causality, availability or an SLO. |
| CI exercises the portfolio across Go, PostgreSQL, React and real Chromium | [Workflow definition](../.github/workflows/ci.yml), [`Makefile`](../Makefile) | Six bounded jobs: verify, PostgreSQL, Web, E2E, measurement and incident | [`CI #58`, all six jobs green](https://github.com/w4llisz/small-chain-platform/actions/runs/41818785830) | A green workflow validates this revision and its synthetic scenarios; it is not production traffic evidence. |

## Five-minute interview path

1. Start with admission: HTTP 202 follows memory acceptance or PostgreSQL
   commit, and an idempotency-key replay returns the same job.
2. Draw the durable attempt protocol: short claim transaction, lease heartbeat,
   owner/version fencing, delayed retry and expired-lease recovery.
3. Use the reviewed crash evidence to explain why worker A can execute once and
   worker B can execute again, while only the current fencing token can persist
   the result.
4. Show the operator console reading the same durable state and lifecycle events,
   then explain the cancellation-versus-completion race.
5. Finish with observability and measurement boundaries: health stays independent
   of optional tracing, and retained evidence is useful only with its source,
   workload and limitations.

## Resume-safe wording

> Built a Go and PostgreSQL background-job platform with bounded concurrency,
> transactional idempotency, lease-based crash recovery, fenced state changes,
> a React operations console, race/integration/E2E tests, and reproducible
> observability evidence.

Use the reviewed 540-job experiment only as an example of the validation
pipeline. Do not turn it into a throughput, latency percentile, availability or
production-scale claim.

## M4 completion checklist

- [x] One-command durable Compose stack with health-ordered startup.
- [x] Database-backed state gauges and bounded worker attempt metrics.
- [x] Optional bounded tracing with a real Collector smoke check.
- [x] Commit-bound measurement bundle with raw inputs and limitations.
- [x] Worker SIGKILL drill with reviewed recovery/fencing evidence.
- [x] Runbook and portfolio claims linked to executable checks.

