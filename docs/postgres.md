# PostgreSQL durable core (M2.1–M2.5)

Implemented: embedded forward-only migrations and an `internal/postgres.Store`
with context-aware admission, claiming, heartbeat, outcome and recovery
operations. `internal/dbworker` composes them into a bounded polling worker for
the trusted checksum executor. PostgreSQL owns durable admission, lease
assignment, fenced state writes and expired-lease transitions. **The HTTP server
and default command still run the M1 memory engine.** There is no database worker
entrypoint, process-level crash-recovery workflow or database-backed HTTP 202 yet.

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

`(job ID, owner, lease version)` is the fencing token lifecycle mutations match.
M2.3 consumes that token, and M2.4 provides the explicit sweep that releases an
expired running row. `ClaimDue` does not silently recover it. This distinction is
important for scheduling and observing recovery work.

## Lifecycle protocol

- `Heartbeat` conditionally extends an unexpired running lease from database
  time. It cannot revive an expired lease.
- `Complete` conditionally stores a bounded result and clears the lease.
- `FailAttempt` atomically stores a bounded error, clears the lease and either
  schedules `retrying` from database time or marks the job `failed` when its
  persisted attempt budget is exhausted.
- `FailPermanent` stores a non-retryable error and immediately fails the job,
  regardless of unused attempt budget.
- `Cancel` is an authoritative operator transition for queued, running and
  retrying work. It increments the lease version before clearing lease fields,
  is idempotent after cancellation, and cannot overwrite a completed terminal
  state.

Worker-owned mutations match ID, owner, version, running state and unexpired
lease in a single statement. Zero updated rows map to `ErrStaleLease`. The
completion/cancellation race therefore has one terminal winner. Transactions
never span task execution, heartbeat intervals or retry delay. Fencing protects
the job row; external handlers still need idempotency because at-least-once work
can perform a side effect before a stale result is rejected.

## Recovery protocol

1. `RecoverExpired` validates a batch size (1–100) and retry delay (0–24 hours).
2. One statement selects expired running rows ordered by `(lease_expires_at, id)`
   with `FOR UPDATE SKIP LOCKED`. A partial index contains only running leases.
3. The same statement moves work with budget remaining to `retrying` at database
   time plus the delay. Exhausted work becomes `failed`.
4. Both paths record `worker lease expired before completion`, increment the
   lease version and clear owner/expiry. Old workers then fail every fenced write.

Multiple sweepers can run concurrently without coordinating in Go. The batch
bound limits lock and return-set size, and no explicit transaction spans another
statement. The database worker invokes this sweep at startup and periodically.
No process-kill test is claimed until that worker has a command entrypoint.

## Worker protocol

`internal/dbworker.Worker` claims only as many rows as it has free execution
slots and executes after `ClaimDue` commits. It heartbeats each active lease,
uses the current claim token for every outcome, and schedules bounded recovery
sweeps independently from polling. A lost heartbeat cancels local work and
suppresses its outcome; the handler contract requires prompt context handling.

On graceful cancellation the polling/recovery loop stops before waiting for
in-flight attempts. Accepted attempts continue their heartbeats and remain
bounded by their persisted timeout while draining. Retryable failures use capped
exponential equal jitter; permanent failures and recovered panics stop before
unused attempt budget. The package is deliberately separate from the default
M1 command until a process-level recovery test can verify its lifecycle.

## Migration contract

`cmd/migrate` embeds numbered SQL files. It obtains a transaction-level advisory
lock before creating/reading `schema_migrations`, verifies the exact applied
prefix and SHA-256 checksums, then applies pending SQL and records history in the
same transaction. Concurrent migrators serialize; a failed migration rolls back
both DDL and history. Unknown/newer or edited history fails closed. The lock
coordinates this project's migrators, not arbitrary manual SQL changes.

Never edit an applied file. Add `0005_*.sql` for the next change. Only small,
transaction-compatible migrations belong here; no `CREATE INDEX CONCURRENTLY`,
destructive automatic rollback, or production online-migration claim is made.

## Evidence and next increment

The real-database suite covers the M2.1 admission cases plus concurrent claims
from two independent pools, uniqueness across 24 jobs, deterministic eligibility,
attempt/version increments, delayed/exhausted/running exclusion, partial-index
shape, input bounds, cancellation, and skipping an explicitly locked queue head.
M2.3 cases cover heartbeat, success, delayed retry, attempt exhaustion,
expired/stale tokens, cancellation fencing/idempotency, migration backfill, and a
two-connection completion/cancel race. M2.4 cases cover database-time retry
delay, persisted budget exhaustion, fencing-version advance, the recovery index,
validation/cancellation, and two concurrent sweepers recovering 24 jobs exactly
once. M2.5 adds permanent-failure fencing and an end-to-end worker case: a task
longer than its initial lease succeeds through heartbeat, an injected transient
failure retries, and an expired claim recovers. These are correctness tests, not
throughput measurements. The integration case runs goroutines against a real
database, not separate OS processes.

Next: expose the database worker through an explicit command mode and kill one
of two worker processes in a recovery test. That is the gate for claiming
process-level crash recovery.
