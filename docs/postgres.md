# PostgreSQL durable core (M2.1–M2.2)

Implemented: embedded forward-only migrations and an `internal/postgres.Store`
with context-aware `Submit`, `Get`, and `ClaimDue`. PostgreSQL now owns durable
admission and atomic lease assignment. **The HTTP server and worker still run the
M1 memory engine.** There is no persistent worker execution, heartbeat,
completion, cancellation, lease recovery or database-backed HTTP 202 yet.

## Reproduce

Use a disposable PostgreSQL 16 database and Go 1.27.x. For example:

```sh
docker run --rm --name small-chain-postgres \
  -e POSTGRES_USER=small_chain -e POSTGRES_PASSWORD=local_demo_only \
  -e POSTGRES_DB=small_chain_test -p 127.0.0.1:5432:5432 -d postgres:16
# Wait until this succeeds:
docker exec small-chain-postgres pg_isready -U small_chain -d small_chain_test
export DATABASE_URL='postgres://small_chain:local_demo_only@localhost:5432/small_chain_test?sslmode=disable'
make migrate
make migrate  # no-op: checksums still checked
export TEST_DATABASE_URL="$DATABASE_URL"
make test-integration
# Stop only this disposable container when finished:
docker stop small-chain-postgres
```

These example credentials are for a local throwaway container. Do not commit a
real connection string. Migration commands require schema-creation privileges;
tests create a random schema per case and drop only that schema at cleanup.
The test database user must be able to create schemas. CI provisions the same
PostgreSQL major version and runs the integration suite with the race detector.
`make verify` remains usable without a database. The explicit
`make test-integration` target **fails**, rather than skips, without its URL.

## Admission protocol

1. Validate the key and normalize the request using the same Go functions as M1.
   Valid UTF-8 text (including escaped NUL) round-trips unchanged. Explicit
   defaults and omitted defaults represent the same request.
2. Hash canonical JSON of the normalized Go spec with SHA-256. Persist the
   fingerprint with the spec; only the **key** is unique. Identical payloads
   submitted with distinct keys are distinct jobs. Full normalized specs are
   compared on replay, so correctness does not rely solely on hash equality.
3. Start an explicit READ COMMITTED transaction. `INSERT ... ON CONFLICT
   (idempotency_key) DO NOTHING RETURNING ...` either inserts or waits for a
   conflicting transaction to resolve.
4. On conflict, use a **separate SELECT statement** to obtain a fresh snapshot.
   Same spec returns the original job; different spec returns `ErrConflict`.
   A single INSERT/SELECT CTE could see a conflict without seeing the winning
   row in its original snapshot. This follows PostgreSQL's documented
   [READ COMMITTED behavior](https://www.postgresql.org/docs/16/transaction-iso.html#XACT-READ-COMMITTED).
5. Return success only after commit. A connection failure during commit has an
   ambiguous outcome; retry the **same key**, never silently generate a new one.

There is no deletion/expiration path in this increment. Keys and jobs persist
indefinitely, including across client/pool reconnects; admission has no retention
cap yet. Before wiring this store into the HTTP API, add a bounded admission and
retention policy with a documented replay window. Concurrent deletion would
require revisiting the two-statement replay protocol. The prototype is not an
unbounded public endpoint.

The pool has at most four connections per Store. Connection acquisition and
operations have a five-second context budget; PostgreSQL also enforces five-
second statement/idle-transaction and two-second lock timeouts. Cleanup uses a
separate bounded context. SQL parameters carry user values; keys and DSNs are
not logged. The stored `spec` uses PostgreSQL `json`, because `jsonb` cannot
represent an escaped NUL accepted by the checksum contract; no spec-field
index/query is used. `max_attempts` is stored separately for claim filtering.
The lease migration backfills it from the canonical JSON text with a narrow
regular expression rather than a JSON extraction operator, because PostgreSQL
JSON extraction rejects an otherwise preserved escaped NUL.

## Claim protocol

1. Validate a stable worker owner, batch size (1–100), and lease duration
   (1 second–15 minutes) before opening a transaction.
2. In one READ COMMITTED transaction, select due queued/retrying rows ordered by
   `(available_at, id)` with `FOR UPDATE SKIP LOCKED`. Exclude rows whose
   persisted attempt budget is exhausted. A matching partial index avoids
   indexing running/terminal history.
3. In the same statement, change candidates to running, increment attempt and
   lease version, and record owner/expiry. `statement_timestamp()` supplies a
   shared database clock; Go never computes lease expiry.
4. Read all claimed rows and commit before returning them. Task execution must
   occur after `ClaimDue` returns. If commit is ambiguous, the method returns no
   claims and the caller must not execute work; a future recovery pass handles
   the stranded lease.

`(job ID, owner, lease version)` is the fencing token future mutations must
match. M2.2 only creates that token. It does not yet heartbeat, finish, retry,
cancel, append events, or reclaim an expired running row. An expired lease is
therefore still unavailable today. This distinction is important: safe parallel
claiming has been implemented; crash recovery has not.

## Migration contract

`cmd/migrate` embeds numbered SQL files. It obtains a transaction-level advisory
lock before creating/reading `schema_migrations`, verifies the exact applied
prefix and SHA-256 checksums, then applies pending SQL and records history in the
same transaction. Concurrent migrators serialize; a failed migration rolls back
both DDL and history. Unknown/newer or edited history fails closed. The lock
coordinates this project's migrators, not arbitrary manual SQL changes.

Never edit an applied file. Add `0003_*.sql` for the next change. Only small,
transaction-compatible migrations belong here; no `CREATE INDEX CONCURRENTLY`,
destructive automatic rollback, or production online-migration claim is made.

## Evidence and next increment

The real-database suite covers the M2.1 admission cases plus concurrent claims
from two independent pools, uniqueness across 24 jobs, deterministic eligibility,
attempt/version increments, delayed/exhausted/running exclusion, partial-index
shape, input bounds, cancellation, and skipping an explicitly locked queue head.
These are correctness tests, not throughput measurements. Reconnect is **not** a
database crash-recovery or worker-recovery test.

Next: implement heartbeat and fenced completion/retry/cancel. Every mutation must
match ID, owner, version and running state; stale tokens affect zero rows. Only
after those primitives exist should a database worker execute checksum tasks.
