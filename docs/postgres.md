# PostgreSQL admission (M2.1)

Implemented: an embedded, forward-only migration and `internal/postgres.Store`
with context-aware `Submit`/`Get`. This is a persistence boundary ready for the
next scheduler increment. **The HTTP server still runs the M1 memory engine.**
There is no persistent worker, lease, heartbeat, recovery or database-backed HTTP
202 yet. The schema deliberately allows only queued jobs with zero attempts;
a later migration will add the execution state/lease invariants together.

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
index/query is needed yet.

## Migration contract

`cmd/migrate` embeds numbered SQL files. It obtains a transaction-level advisory
lock before creating/reading `schema_migrations`, verifies the exact applied
prefix and SHA-256 checksums, then applies pending SQL and records history in the
same transaction. Concurrent migrators serialize; a failed migration rolls back
both DDL and history. Unknown/newer or edited history fails closed. The lock
coordinates this project's migrators, not arbitrary manual SQL changes.

Never edit an applied file. Add `0002_*.sql` for the next change. Only small,
transaction-compatible migrations belong here; no `CREATE INDEX CONCURRENTLY`,
destructive automatic rollback, or production online-migration claim is made.

## Evidence and next increment

The real-database suite covers concurrent migrations, checksum drift, unknown
history, DDL rollback, concurrent same/different requests across two pools,
explicit/implicit defaults, waiting on an uncommitted winner (both commit and
rollback), reconnect/read/replay, fingerprint storage, invalid requests, canceled
admission, and missing records. Reconnect is **not** a database crash-recovery or
worker-recovery test. Those remain M2 acceptance criteria.

Next: add the lease schema and transactional claim operation with
`FOR UPDATE SKIP LOCKED`, persisted attempt limits and monotonically increasing
lease versions. Verify two independent clients cannot claim the same job before
introducing worker execution and heartbeats.
