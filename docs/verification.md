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
