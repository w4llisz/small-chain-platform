# M1 verification record

Local verification for this change: 2026-09-30, Linux amd64, Go 1.27.1.

| Check | Observed result |
| --- | --- |
| `make verify` | Passed formatting, vet, race tests, static build and process smoke |
| Job engine package coverage | 92.5% statements; see CI for subsequent changes |
| HTTP API package coverage | 95.4% statements |
| `make fuzz` | Passed a 10-second normalization fuzz run, 114,593 executions, no failing input |
| Real process smoke | Passed retry/result, idempotency, SIGTERM drain and forced-deadline cancellation |
| Docker | No Docker engine available locally; workflow includes a build check |
| GitHub Actions | Inspect the PR's actual checks; local results do not imply remote success |

The command package is exercised by an external process smoke test, so its code is not included in the unit test coverage counters. Package coverage above is not a repository-wide coverage claim. The fuzz execution count is input-testing evidence, not a service throughput benchmark.

Tests focus on concurrent idempotency, worker/queue bounds, safe admission failure, retry classification/exhaustion, panic containment, late-success timeout rejection, cancellation in three nonterminal states, shutdown races, terminal immutability, retention limits, HTTP errors and a real HTTP lifecycle. No durability, multi-process recovery or performance claim has been tested in M1.

Reproduce with `make verify` and `make fuzz`. CI uses four fuzz workers to bound resource use; execution count will vary by machine.

## M2.1 admission increment — 2026-10-01

Local: Linux amd64, Go 1.27.1. `make verify` passed formatting, vet,
race tests, build and real-process smoke. The PostgreSQL integration suite also
compiled with `go test -tags=integration -run='^$' ./internal/postgres`; that
compile-only command is **not** evidence of database execution.

This workspace cannot run a native PostgreSQL service. The `postgres` CI job
therefore provisions a real PostgreSQL 16 service and runs
`make test-integration` with `-race`, then invokes the migration command twice.
Before main is advanced, the candidate is checked on a `feat/` branch; inspect
the candidate commit's actual [CI checks](https://github.com/w4llisz/small-chain-platform/actions/workflows/ci.yml)
for the integration result. The other CI job continues to check the M1 service,
fuzzing and Docker build. Untagged coverage excludes integration tests and does
not represent durable-store test coverage.

Database tests are listed in [the PostgreSQL guide](postgres.md). They test
transactional admission and reconnect persistence, not worker or database
crash recovery. No performance measurement is claimed.

The first candidate's [PostgreSQL job](https://github.com/w4llisz/small-chain-platform/actions/runs/36848797587/job/110325282403)
passed all four integration test groups (including their subcases) and both
migration-command executions. Its container build exposed a missing `go.sum`
entry in the existing `.dockerignore` allowlist; the follow-up build fix includes
that file in the Docker context. Main promotion waits for the fixed candidate's
full CI result. Local fuzzing also passed (10-second budget, 434,357 executions);
this is input-validation evidence, not throughput.

## M2.2 claim increment — 2026-10-02

Local, Linux amd64 with Go 1.27.1: the integration-tagged PostgreSQL package
compiled and passed `go vet`; this is not a substitute for database execution.
The candidate branch CI provisions PostgreSQL 16 and runs the complete suite with
the race detector before main is advanced. New real-DB cases exercise two-pool
concurrent claiming, `SKIP LOCKED`, eligibility/order, attempt/version changes,
the ready partial index, input bounds and canceled contexts. `make verify`, fuzz,
module verification and container build remain required in the same workflow.

No heartbeat, fenced completion, expired-lease recovery, database worker or
performance result is claimed by this increment.

## M2.3 fenced lifecycle increment — 2026-10-03

Local, Linux amd64 with Go 1.27.1: `make verify` passed formatting, vet, race
tests, static build and the real-process smoke workflow. The integration-tagged
PostgreSQL package is also compiled locally before submission. Database execution
is delegated to candidate-branch CI, which provisions PostgreSQL 16 and runs the
complete suite with the race detector; inspect the candidate commit's actual
checks rather than treating compilation as database evidence.

New real-database cases cover heartbeat from database time, fenced success,
delayed retry and attempt exhaustion, expired/stale lease rejection, authoritative
cancel with version advance, idempotent cancellation, a two-connection
completion/cancel race, and upgrade backfill for legacy terminal states. No
expired-lease recovery, database-backed worker/HTTP path, exactly-once side
effects or performance result is claimed.

## M2.4 expired-lease recovery increment — 2026-10-04

Local, Linux amd64 with Go 1.27.1: the integration-tagged PostgreSQL package is
compiled and vetted before submission, and `make verify` covers formatting, vet,
race tests, static build and the real-process M1 smoke workflow. Real database
execution remains a candidate-branch CI gate: PostgreSQL 16 runs the complete
integration suite with the race detector before main is advanced.

New database cases exercise live-versus-expired eligibility, database-time retry
delay, persisted budget exhaustion, version advance and lease clearing, stale
completion rejection for every recovered claim, the partial recovery index,
input/cancellation behavior, and two independent sweepers recovering 24 rows
exactly once. This proves the recovery transition, not a process-kill workflow:
the database worker loop is still planned. No throughput or exactly-once claim is
made.

## M2.5 database worker increment — 2026-10-05

Local, Linux amd64 with Go 1.27.1: `make verify` passed formatting, vet, all
unit/HTTP tests under the race detector, static build and the real-process M1
smoke workflow. The new `internal/dbworker` unit suite reported 80.5% statement
coverage and exercises free-slot polling, the concurrency bound, success,
retryable/permanent outcomes, panic containment, heartbeat-loss cancellation,
UTF-8 error bounds, and shutdown-before-drain ordering. The race detector found
and the implementation fixed a claim/executor read-write race during development.

The integration-tagged packages are compiled locally before submission. A
candidate-branch CI run is the real PostgreSQL 16 gate and runs both the store
and worker suites with `-race`. Its new scenario proves lease renewal for an
attempt longer than the initial lease, persisted retry, and expired-claim
recovery. This does not yet prove OS-process kill/restart behavior, durable HTTP
admission, exactly-once effects or throughput.

## M2.6 process recovery increment — 2026-10-06

Local, Linux amd64 with Go 1.27.1: `make verify` passed formatting, vet, race
tests, both static binaries and the existing real-process M1 smoke workflow.
The worker command reported 56.8% statement coverage from argument validation;
the process lifecycle is exercised externally. A 10-second fuzz run passed
272,879 executions. The integration-tagged packages and process test compile and
pass vet locally; database execution remains gated by candidate CI with a
disposable PostgreSQL 16 schema.

The new integration case builds the worker with the Go race detector, starts two
Linux child processes with distinct owners, waits until worker A has a persisted
lease, starts worker B, then kills A with SIGKILL. It requires B to recover and
complete the job on attempt two, checks the fencing-version sequence, rejects
A's captured completion token, and verifies B exits cleanly on SIGTERM. This is
process recovery evidence for the trusted checksum handler. It does not prove
durable HTTP admission, arbitrary external side-effect idempotency, exactly-once
execution or performance.

## M2.7 bounded admission and retention increment — 2026-10-07

Local, Linux amd64 with Go 1.27.1: `make verify`, integration-tag compilation,
integration-tag vet and module verification are required before submission. The
real PostgreSQL 16 suite remains a candidate-branch CI gate. New database cases
configure a two-record cap, prove replays/conflicts still work while new keys are
rejected, delete only terminal history beyond the replay window, reuse a key only
after deletion, and race 16 admissions against cleanup through two pools while
checking both the cap and retained counter. These are correctness checks; no
throughput, production retention cadence or durable HTTP claim is made.

## M2.8 durable HTTP increment — 2026-10-08

Local, Linux amd64 with Go 1.27.1: `make verify`, integration-tag compilation,
integration-tag vet and module verification are required before submission.
The database-backed API is exercised by candidate-branch CI against PostgreSQL
16 with the race detector and a real race-instrumented server binary.

The new process case obtains 202 only after a durable submit, maps a one-record
database cap to 503, stops the API with SIGTERM, restarts it on the same schema,
and verifies same-key replay, lookup, changed-payload conflict, cancellation and
the PostgreSQL backend metric. The server never creates a memory engine in this
mode. This proves API-process restart persistence, not database disaster
recovery, scheduled cleanup, exactly-once side effects or throughput.

## M2.9 retention maintenance increment — 2026-10-09

Local, Linux amd64 with Go 1.27.2: `make verify`, the focused maintenance race
tests, integration-tag compilation/vet and module verification are required
before submission. Candidate-branch CI runs the real PostgreSQL 16 suite.

The retention runner performs one startup sweep and serial periodic sweeps,
logs a failed call before retrying on the next cadence, and exits when its
in-flight store call observes cancellation. The real API process case ages a
canceled record outside the configured replay window, waits for the scheduled
bounded purge to release a one-record cap, and then requires clean SIGTERM
shutdown. Readiness remains tied to draining/database connectivity rather than
the previous maintenance result. No retention throughput or production cadence
claim is made.

## M2.10 transactional event foundation — 2026-10-10

Local, Linux amd64 with Go 1.27.2: `make verify`, a 10-second normalization fuzz
run, integration-tag compilation/vet and `go mod verify` passed. The real
PostgreSQL 16 suite remains the candidate-branch CI gate.

Migration `0006_job_events.sql` backfills only the provable admission event for
older rows. New admissions write one event even under 32-way same-key
concurrency, and two concurrent claimers write one event for each of 24 distinct
fencing versions. Failure-injection triggers make submitted or claimed event
inserts fail; the tests require the corresponding job row/capacity counter or
claim state/attempt/version to remain unchanged. Outcome events and event reads
are not yet implemented, and no event throughput claim is made.

## M2.11 complete lifecycle events — 2026-10-11

Local, Linux amd64 with Go 1.27.2: `make verify`, integration-tag compilation,
integration-tag vet and module verification are required before submission. The
real PostgreSQL 16 suite remains the candidate-branch CI gate.

Migration `0007_outcome_events.sql` adds resulting-state and bounded-message
evidence plus uniqueness constraints for worker outcomes, recovery fences and
terminal transitions. Success, retry scheduling, worker failure, expired-lease
recovery and cancellation now insert an event in the same statement as their
state update; heartbeats remain intentionally absent. Integration tests inject
a rejected success event and require the job to stay running, count concurrent
recovery events, and require a completion/cancel race to produce exactly one
terminal event. Event throughput measurements are not implemented.

## M2.12 bounded event reads — 2026-10-12

Local, Linux amd64 with Go 1.27.2: `make verify`, integration-tag compilation,
integration-tag vet and module verification are required before submission. The
candidate-branch workflow runs the full race-enabled PostgreSQL 16 integration
suite, including the real API process.

The new store case proves a three-event lifecycle crosses a two-row page without
duplication, preserves outcome state and fencing fields, returns a non-null
empty final page, distinguishes a missing job and rejects invalid bounds. The
process case reads submitted and canceled events through the HTTP cursor across
a server restart. These checks prove bounded cursor semantics against the
project's test workload; they do not claim snapshot export across requests,
indefinite audit retention or measured query throughput.

## M3.1 bounded job-list API — 2026-10-13

Local, Linux amd64 with Go 1.27.2: `make verify`, integration-tag compilation,
integration-tag vet and module verification are required before submission. The
candidate workflow runs PostgreSQL 16 with the race detector and the real API
process.

The store case creates deterministic recent-job ordering, crosses a two-row
page, inserts a newer row between requests and proves the later page neither
duplicates nor shifts. It also checks state filtering, non-null empty results,
input bounds and both list index shapes. The process case paginates queued jobs
after API restart, rejects reuse of a cursor under another state filter and
finds a canceled job through the filter. This proves contract correctness for
the test workload, not a cross-request snapshot or measured query latency.

## M3.5 real-stack operator workflow — 2026-10-21

Local, Linux amd64 with Go 1.27.2 and Node.js 24.19.0: `make verify`, all 32
Vitest client/component tests, TypeScript checking, the Vite production build,
shell syntax checking and Playwright test discovery passed. This local runtime
did not provide PostgreSQL or a Chromium installation, so it did not execute
the real-stack browser case and makes no local E2E-pass claim.

The candidate workflow provisions PostgreSQL 16 and Chromium, applies the real
migrations, then starts the compiled API, one database worker and Vite. Its one
30-second Playwright case submits a delayed job, waits for a committed running
lease, finds that job through the Running filter, inspects its claimed event and
owner, confirms cancellation, and requires a persisted canceled state from the
API. The CI result is the acceptance evidence; traces and screenshots are
retained only on failure. This proves one Chromium operator path, not visual
fidelity across browsers or production deployment.

## M4.1 durable state gauges — 2026-10-22

Local, Linux amd64 with Go 1.27.2: the focused HTTP/PostgreSQL packages run with
the race detector, the full `make verify` suite and integration-tag compilation
are required before submission. The local runtime has no PostgreSQL service, so
the real database assertion remains a candidate-commit CI gate.

The store test creates one queued and one canceled record and requires explicit
zeroes for the other four states from a single database snapshot. The API unit
case injects a state-read failure and requires HTTP 200, an `up` gauge of zero,
and no potentially stale `small_chain_jobs` series. The existing real API
process test now requires two queued and zero canceled rows after retention.
These gauges describe retained state at scrape time; they do not measure queue
latency, execution duration, throughput or an SLO.

## M4.2 durable-worker attempt histogram — 2026-10-23

Local, Linux amd64 with Go 1.27.2: `make verify` passed formatting, vet, all Go
tests under the race detector, static builds and the real-process memory-mode
smoke test. Integration-tagged PostgreSQL, worker-process and HTTP packages
compiled and passed vet; `go mod verify` also passed. The local runtime did not
provide PostgreSQL, so those compile-only checks are not database execution
evidence.

Focused tests execute three attempts concurrently and require one metric
observation for each success, retryable failure and recovered panic. Direct
histogram tests require exact cumulative bucket, `+Inf`, sum and count output,
including an observation beyond the accepted 30-second task timeout. The
command handler test requires Prometheus text content on `GET /metrics` and a
405 response for `POST`. The histogram describes process-local trusted executor
wall time; it resets on restart and excludes queue wait and every database call.
No percentile, workload latency, throughput or SLO result is claimed.

[CI run #46](https://github.com/w4llisz/small-chain-platform/actions/runs/39943382842)
completed successfully on 2026-10-23: `verify`, PostgreSQL integration,
frontend verification and the real-stack Chromium E2E job all passed.

## M4.3 health-ordered Compose demo — 2026-10-24

Local, Linux amd64 with Go 1.27.2: `make verify` passed formatting, vet, all Go
tests under the race detector, four static command builds and the real-process
memory smoke test. The health probe unit test, Compose YAML parsing, Compose
smoke shell syntax, integration-tag compilation/vet and module verification also
passed. This runtime does not provide Docker, so it cannot execute the stack
locally; no local Compose-pass claim is made.

The CI smoke case validates the Compose model, builds the shared non-root
scratch image, waits for PostgreSQL health and a successful one-shot migration,
then checks API readiness and worker metrics. It submits one checksum task,
requires the durable API to report `succeeded`, and requires both authoritative
database metric collection and at least one worker-duration observation. A
bounded failure path prints container state/logs, and the exit trap always
removes the isolated project's containers and volume. This is startup and
single-task evidence for the local demo topology, not a load, failover or
production deployment result.

## M4.4 provisioned local observability — 2026-10-25

Local checks passed shell syntax, YAML parsing, dashboard JSON parsing and
`git diff --check`. This runtime provides neither the Go toolchain nor Docker
Compose: `make verify` stopped immediately because `gofmt` and `go` were not
found, and `docker compose config` was skipped. Only CI can execute those checks
and the new images; no local Go, Compose, Prometheus or Grafana pass is claimed.

The extended Compose smoke test has a 60-second bound per endpoint or query. It
requires Prometheus to report both application targets up, a successful durable
state read and at least one completed worker attempt after a real submitted
job. It then retrieves the immutable `small-chain-overview` dashboard through
Grafana's anonymous Viewer API and checks its UID and title. This is wiring and
provisioning evidence. The dashboard's count, mean and fixed-bucket panels are
not a performance benchmark, percentile, availability result or production
SLO.

## M4.5a bounded load generator — 2026-10-26

The deterministic HTTP suite drives ten requests through a three-request
ceiling and requires the observed server concurrency to equal that ceiling.
It verifies unique ordered idempotency keys, exact classification of 202, 200,
429, record-capacity 503 and 500 responses, monotonic cumulative buckets and a
versioned payload hash. A transport double fails every call and proves four
configured requests produce four visible transport errors without retries.
Table-driven validation covers URL scope and every request/task bound.

Local Linux amd64 verification uses Go 1.27.2. The focused race test passed and
the new `internal/loadgen` package reported 90.1% statement coverage. Full
`make verify` passed vet, all race tests, five static command builds and the
real-process smoke test. During that run, `check-fmt` exposed that a missing
`gofmt` executable could previously print an error but still satisfy the empty
output test; the target now fails explicitly and supports a `GOFMT` override.
A functional command smoke sent 12 uniquely keyed requests at concurrency three
to the real memory-mode API; all 12 were accepted, the outcome sum equaled the
offered count and the `+Inf` bucket contained all observations. This only checks
the report contract and is not retained as performance evidence. M4.5b remains
the measured-report step.

## M4.5b1 raw-evidence harness — 2026-10-27

The collector statically passed Bash syntax, workflow YAML parsing and
`git diff --check`. Local Go verification passed with Go 1.27.2: formatting,
vet, race tests, five static command builds and the real-process smoke test.
This runtime has no Docker daemon, so it did not execute the Compose matrix;
the main-push measurement job is the first end-to-end collection authority.

The harness itself requires nine complete 60-request reports, exact outcome
accounting, all 540 durable jobs in `succeeded`, no active durable jobs and at
least 540 observed worker attempts. It retains raw reports, Prometheus query
responses, environment metadata, final container snapshots and hashes, while
its bounded failure path captures logs before deleting isolated volumes. These
checks prove evidence integrity and convergence for this synthetic workload.
They do not establish portable throughput, a percentile, availability or an
SLO. A measured report remains M4.5b2.

## M4.5b2a offline evidence verification — 2026-10-28

The Go validator has deterministic fixtures for a complete evidence bundle and
requires its provenance, scenario matrix, exact admission accounting,
Prometheus convergence and non-empty runtime snapshots. Negative cases prove a
changed scenario is rejected by SHA-256, rehashed queue rejection still fails
the semantic checks, and active durable work fails convergence validation.

Local Go 1.27.2 `make verify` covers formatting, vet, all race tests, six static
commands and the real-process smoke test. Bash syntax, workflow YAML parsing
and `git diff --check` also pass. No Docker daemon is available locally, so the
real measurement artifact and its report remain CI-dependent and are not
claimed here.

## M4.5b2b commit-bound provenance — 2026-10-29

The evidence command now requires `-expected-commit` and defaults to rejecting
artifacts whose hashed context records tracked-tree changes. The Compose
collector supplies its own full HEAD and explicitly requires a clean tree.
Race-tested negative cases use otherwise valid, fully rehashed fixtures to
prove a different expected commit and a dirty tree are rejected.

Local Go 1.27.2 `make verify` passes formatting, vet, all race tests, six static
command builds and the real-process smoke test. Bash syntax and
`git diff --check` also pass. The environment still has no Docker daemon, and
GitHub Actions did not start for the two preceding connector-authored commits;
no Compose measurement or artifact result is claimed.
