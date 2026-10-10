//go:build integration

package postgres

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

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
	var attempt int
	var sameTimestamp bool
	if err := store.pool.QueryRow(context.Background(), `SELECT e.kind, e.attempt, e.created_at = j.created_at
FROM job_events AS e JOIN jobs AS j ON j.id = e.job_id
WHERE j.idempotency_key = 'before-events'`).Scan(&kind, &attempt, &sameTimestamp); err != nil {
		t.Fatal(err)
	}
	if kind != "submitted" || attempt != 0 || !sameTimestamp {
		t.Fatalf("backfilled event kind=%q attempt=%d same_timestamp=%t", kind, attempt, sameTimestamp)
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
}
