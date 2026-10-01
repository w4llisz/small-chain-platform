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
