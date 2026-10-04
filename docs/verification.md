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
