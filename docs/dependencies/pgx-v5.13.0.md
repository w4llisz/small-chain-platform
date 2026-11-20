# pgx v5.13.0 review

Reviewed on 2026-11-20 for [PR #7](https://github.com/w4llisz/small-chain-platform/pull/7),
head `7727c34e6b5d4ddb2d32565d1b46900af4a7759c`. The change replaces only pgx
v5.12.0 with v5.13.0 and its two go.sum entries; no indirect dependency or schema
migration changes. The new minimum reviewed version is enforced by
[scripts/test_release_candidate.py](../../scripts/test_release_candidate.py).

## Applicability

Source: the tagged upstream [v5.13.0 changelog](https://github.com/jackc/pgx/blob/v5.13.0/CHANGELOG.md).

| Upstream change | Repository path and assessment |
| --- | --- |
| Malformed row responses can panic in `Rows.Values`; fixed by validating field/value counts | No `Rows.Values` call was found in repository Go sources. Durable queries use `Scan`, for example [readJob](../../internal/postgres/store.go), [job listing](../../internal/postgres/job_list.go) and [events](../../internal/postgres/event_read.go). This is a dependency hardening update, not a demonstrated exploit in this application. |
| Connection-closed errors while receiving a response are no longer classified as safe to retry | The service uses native pgxpool, not `pgx/v5/stdlib` or `sql.Open`. Imports of `database/sql` provide nullable value types. The upstream automatic `database/sql` retry scenario is not the service's call path. An ambiguous submission commit still requires replaying the same idempotency key; a driver update cannot establish exactly-once execution. |
| File-backed TLS client certificates reload for each certificate request | [Store.Open](../../internal/postgres/store.go) passes parsed DSN configuration to pgxpool without overriding or replacing TLSConfig. Local Compose uses `sslmode=disable`, so it does not exercise certificate rotation. A user-supplied `sslcert`/`sslkey` DSN can opt into the changed behavior: files must remain readable for later connections, and symlink rotation is followed. Production mTLS/rotation is not verified by this review. |
| Range encoding and database/sql precision/length metadata corrections | No range-codec or database/sql driver path was found in application code. No API or migration changes are needed for the current task model. |

## Evidence and integration boundary

The existing PR workflow [44306843295](https://github.com/w4llisz/small-chain-platform/actions/runs/44306843295)
passed `verify`, `postgres`, `web` and `e2e` for the reviewed PR head. This includes
real PostgreSQL integration and browser checks. The seven main-only evidence
and packaging jobs were skipped, not passed. PR evidence predates the latest
main quick-start smoke increment; the combined revision must run its own CI.

The upgrade preserves the original dependency commit as a merge parent rather
than rewriting its branch. See [verification](../verification.md) for the local
checks on the combined tree and their limits. This review does not publish a
release, prove a production TLS deployment or claim that all dependency defects
have been excluded.
