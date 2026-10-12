//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

func TestListEventsUsesBoundedForwardCursor(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	claim := submitAndClaim(t, store, "event-page", jobs.Spec{Kind: "demo.checksum"})
	if _, err := store.Complete(context.Background(), claim, "done"); err != nil {
		t.Fatal(err)
	}

	first, err := store.ListEvents(context.Background(), claim.Job.ID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || first.Events[0].Kind != "submitted" ||
		first.Events[1].Kind != "claimed" || first.NextCursor == "" {
		t.Fatalf("first page: %+v", first)
	}
	cursor, err := strconv.ParseInt(first.NextCursor, 10, 64)
	if err != nil || first.NextCursor != first.Events[1].ID {
		t.Fatalf("cursor=%q events=%+v err=%v", first.NextCursor, first.Events, err)
	}
	second, err := store.ListEvents(context.Background(), claim.Job.ID, cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.Events[0].Kind != "succeeded" ||
		second.Events[0].State != jobs.Succeeded || second.Events[0].LeaseOwner != claim.Owner ||
		second.Events[0].LeaseVersion != claim.Version || second.NextCursor != "" {
		t.Fatalf("second page: %+v", second)
	}
	lastID, err := strconv.ParseInt(second.Events[0].ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.ListEvents(context.Background(), claim.Job.ID, lastID, 2)
	if err != nil || len(empty.Events) != 0 || empty.Events == nil || empty.NextCursor != "" {
		t.Fatalf("empty page: %+v err=%v", empty, err)
	}

	if _, err := store.ListEvents(context.Background(), strings.Repeat("a", 32), 0, 1); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("missing job: %v", err)
	}
	for _, test := range []struct {
		id     string
		cursor int64
		limit  int
	}{
		{"bad", 0, 1},
		{claim.Job.ID, -1, 1},
		{claim.Job.ID, 0, 0},
		{claim.Job.ID, 0, MaxEventPageSize + 1},
	} {
		if _, err := store.ListEvents(context.Background(), test.id, test.cursor, test.limit); !errors.Is(err, jobs.ErrInvalid) {
			t.Fatalf("id=%q cursor=%d limit=%d: %v", test.id, test.cursor, test.limit, err)
		}
	}
}

type storedEvent struct {
	kind         string
	state        jobs.State
	attempt      int
	leaseOwner   sql.NullString
	leaseVersion sql.NullInt64
	message      sql.NullString
}

func readEvents(t *testing.T, store *Store, jobID string) []storedEvent {
	t.Helper()
	rows, err := store.pool.Query(context.Background(), `SELECT kind, state, attempt,
       lease_owner, lease_version, message
FROM job_events WHERE job_id = $1 ORDER BY id`, jobID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []storedEvent
	for rows.Next() {
		var event storedEvent
		if err := rows.Scan(&event.kind, &event.state, &event.attempt,
			&event.leaseOwner, &event.leaseVersion, &event.message); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func TestEventMigrationBackfillsAdmissionOnly(t *testing.T) {
	store, _ := database(t)
	prefix := fstest.MapFS{}
	for _, name := range []string{
		"migrations/0001_admission.sql",
		"migrations/0002_leases.sql",
		"migrations/0003_outcomes.sql",
		"migrations/0004_recovery.sql",
		"migrations/0005_retention.sql",
	} {
		data, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		prefix[name] = &fstest.MapFile{Data: data}
	}
	if err := store.migrate(context.Background(), prefix); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(context.Background(), `INSERT INTO jobs
        (id, idempotency_key, request_fingerprint, spec, max_attempts)
        VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'before-events', decode(repeat('00', 32), 'hex'),
                '{"kind":"demo.checksum","payload":"","max_attempts":3,"timeout_ms":1000}', 3)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var kind string
	var state jobs.State
	var attempt int
	var sameTimestamp bool
	if err := store.pool.QueryRow(context.Background(), `SELECT e.kind, e.state, e.attempt, e.created_at = j.created_at
FROM job_events AS e JOIN jobs AS j ON j.id = e.job_id
WHERE j.idempotency_key = 'before-events'`).Scan(&kind, &state, &attempt, &sameTimestamp); err != nil {
		t.Fatal(err)
	}
	if kind != "submitted" || state != jobs.Queued || attempt != 0 || !sameTimestamp {
		t.Fatalf("backfilled event kind=%q state=%q attempt=%d same_timestamp=%t", kind, state, attempt, sameTimestamp)
	}
}

func TestLifecycleEventsRecordOutcomesWithoutHeartbeats(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)

	completed := submitAndClaim(t, store, "events-complete", jobs.Spec{Kind: "demo.checksum"})
	if _, err := store.Heartbeat(context.Background(), completed, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := len(readEvents(t, store, completed.Job.ID)); got != 2 {
		t.Fatalf("events after heartbeat=%d, want submission and claim only", got)
	}
	if _, err := store.Complete(context.Background(), completed, "done"); err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, store, completed.Job.ID)
	if len(events) != 3 || events[2].kind != "succeeded" || events[2].state != jobs.Succeeded ||
		events[2].attempt != 1 || events[2].leaseOwner.String != completed.Owner ||
		events[2].leaseVersion.Int64 != completed.Version || events[2].message.Valid {
		t.Fatalf("completion events: %+v", events)
	}

	first := submitAndClaim(t, store, "events-retry", jobs.Spec{Kind: "demo.checksum", MaxAttempts: 2})
	if _, err := store.FailAttempt(context.Background(), first, "try again", 0); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(context.Background(), "events-retry-worker", 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("retry claim: %+v, %v", claims, err)
	}
	if _, err := store.FailAttempt(context.Background(), claims[0], "budget exhausted", 0); err != nil {
		t.Fatal(err)
	}
	events = readEvents(t, store, first.Job.ID)
	if len(events) != 5 || events[2].kind != "retry_scheduled" || events[2].state != jobs.Retrying ||
		events[2].message.String != "try again" || events[4].kind != "failed" ||
		events[4].state != jobs.Failed || events[4].message.String != "budget exhausted" {
		t.Fatalf("retry events: %+v", events)
	}

	recovered := submitAndClaim(t, store, "events-recovery", jobs.Spec{Kind: "demo.checksum", MaxAttempts: 2})
	expireLease(t, store, recovered.Job.ID)
	if got, err := store.RecoverExpired(context.Background(), 1, 0); err != nil || len(got) != 1 {
		t.Fatalf("recover: %+v, %v", got, err)
	}
	events = readEvents(t, store, recovered.Job.ID)
	if len(events) != 3 || events[2].kind != "lease_recovered" || events[2].state != jobs.Retrying ||
		events[2].leaseOwner.String != recovered.Owner ||
		events[2].leaseVersion.Int64 != recovered.Version+1 ||
		events[2].message.String != leaseExpiredMessage {
		t.Fatalf("recovery events: %+v", events)
	}

	canceled, _, err := store.Submit(context.Background(), "events-cancel", jobs.Spec{Kind: "demo.checksum"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(context.Background(), canceled.ID, "operator request"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(context.Background(), canceled.ID, "ignored replay"); err != nil {
		t.Fatal(err)
	}
	events = readEvents(t, store, canceled.ID)
	if len(events) != 2 || events[1].kind != "canceled" || events[1].state != jobs.Canceled ||
		events[1].attempt != 0 || events[1].leaseOwner.Valid ||
		events[1].leaseVersion.Int64 != 1 || events[1].message.String != "operator request" {
		t.Fatalf("cancellation events: %+v", events)
	}
}

func TestEventFailureRollsBackStateTransition(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	if _, err := store.pool.Exec(context.Background(), `CREATE FUNCTION reject_test_event() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'event insert blocked by test'; END; $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(context.Background(), `CREATE TRIGGER reject_submitted_event
BEFORE INSERT ON job_events FOR EACH ROW WHEN (NEW.kind = 'submitted')
EXECUTE FUNCTION reject_test_event()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Submit(context.Background(), "event-rollback", jobs.Spec{Kind: "demo.checksum"}); err == nil {
		t.Fatal("submit succeeded without its event")
	}
	var jobsCount, eventsCount, retainedJobs int
	if err := store.pool.QueryRow(context.Background(), `SELECT
    (SELECT count(*) FROM jobs),
    (SELECT count(*) FROM job_events),
    (SELECT retained_jobs FROM admission_control WHERE singleton)`).Scan(&jobsCount, &eventsCount, &retainedJobs); err != nil {
		t.Fatal(err)
	}
	if jobsCount != 0 || eventsCount != 0 || retainedJobs != 0 {
		t.Fatalf("submit rollback jobs=%d events=%d retained=%d", jobsCount, eventsCount, retainedJobs)
	}
	if _, err := store.pool.Exec(context.Background(), "DROP TRIGGER reject_submitted_event ON job_events"); err != nil {
		t.Fatal(err)
	}

	job, replay, err := store.Submit(context.Background(), "event-rollback", jobs.Spec{Kind: "demo.checksum"})
	if err != nil || replay {
		t.Fatalf("submit after trigger removal: replay=%t err=%v", replay, err)
	}
	if _, err := store.pool.Exec(context.Background(), `CREATE TRIGGER reject_claimed_event
BEFORE INSERT ON job_events FOR EACH ROW WHEN (NEW.kind = 'claimed')
EXECUTE FUNCTION reject_test_event()`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDue(context.Background(), "event-worker", 1, time.Minute); err == nil {
		t.Fatal("claim succeeded without its event")
	}
	var state jobs.State
	var attempts int
	var version int64
	if err := store.pool.QueryRow(context.Background(), `SELECT state, attempts, lease_version
FROM jobs WHERE id = $1`, job.ID).Scan(&state, &attempts, &version); err != nil {
		t.Fatal(err)
	}
	if state != jobs.Queued || attempts != 0 || version != 0 {
		t.Fatalf("claim rollback state=%s attempts=%d version=%d", state, attempts, version)
	}
	if err := store.pool.QueryRow(context.Background(), "SELECT count(*) FROM job_events WHERE job_id = $1", job.ID).Scan(&eventsCount); err != nil {
		t.Fatal(err)
	}
	if eventsCount != 1 {
		t.Fatalf("events after rejected claim=%d, want submitted only", eventsCount)
	}
	if _, err := store.pool.Exec(context.Background(), "DROP TRIGGER reject_claimed_event ON job_events"); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(context.Background(), "event-worker", 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim after trigger removal: %+v err=%v", claims, err)
	}
	var kind, owner string
	if err := store.pool.QueryRow(context.Background(), `SELECT kind, attempt, lease_owner, lease_version
FROM job_events WHERE job_id = $1 AND kind = 'claimed'`, job.ID).Scan(&kind, &attempts, &owner, &version); err != nil {
		t.Fatal(err)
	}
	if kind != "claimed" || attempts != 1 || owner != "event-worker" || version != claims[0].Version {
		t.Fatalf("claimed event kind=%q attempt=%d owner=%q version=%d", kind, attempts, owner, version)
	}
	if _, err := store.pool.Exec(context.Background(), `CREATE TRIGGER reject_succeeded_event
BEFORE INSERT ON job_events FOR EACH ROW WHEN (NEW.kind = 'succeeded')
EXECUTE FUNCTION reject_test_event()`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), claims[0], "must roll back"); err == nil {
		t.Fatal("completion succeeded without its event")
	}
	var result sql.NullString
	if err := store.pool.QueryRow(context.Background(), `SELECT state, result
FROM jobs WHERE id = $1`, job.ID).Scan(&state, &result); err != nil {
		t.Fatal(err)
	}
	if state != jobs.Running || result.Valid {
		t.Fatalf("completion rollback state=%s result=%v", state, result)
	}
	if got := len(readEvents(t, store, job.ID)); got != 2 {
		t.Fatalf("events after rejected completion=%d, want submitted and claimed", got)
	}
}
