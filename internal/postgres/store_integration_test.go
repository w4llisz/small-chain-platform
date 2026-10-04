//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

// Every test owns a disposable schema; never truncate a shared database.
func database(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests (use a disposable PostgreSQL database)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("sc_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		admin.Close(ctx)
	})
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("TEST_DATABASE_URL must be a postgres:// URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	q.Set("application_name", schema)
	u.RawQuery = q.Encode()
	store, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store, u.String()
}

func migrate(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrations(t *testing.T) {
	s, dsn := database(t)
	second, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*Store{s, second} {
		go func() { <-start; results <- store.Migrate(context.Background()) }()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	migrate(t, s)
	var count int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 4 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	admission, err := migrations.ReadFile("migrations/0001_admission.sql")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := migrations.ReadFile("migrations/0002_leases.sql")
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := migrations.ReadFile("migrations/0003_outcomes.sql")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := migrations.ReadFile("migrations/0004_recovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	altered := fstest.MapFS{
		"migrations/0001_admission.sql": {Data: append(append([]byte{}, admission...), '\n')},
		"migrations/0002_leases.sql":    {Data: leases},
		"migrations/0003_outcomes.sql":  {Data: outcomes},
		"migrations/0004_recovery.sql":  {Data: recovery},
	}
	if err := s.migrate(context.Background(), altered); err == nil || !strings.Contains(err.Error(), "history mismatch") {
		t.Fatalf("edited migration: %v", err)
	}
	broken := fstest.MapFS{
		"migrations/0001_admission.sql": {Data: admission},
		"migrations/0002_leases.sql":    {Data: leases},
		"migrations/0003_outcomes.sql":  {Data: outcomes},
		"migrations/0004_recovery.sql":  {Data: recovery},
		"migrations/0005_broken.sql":    {Data: []byte("CREATE TABLE rollback_probe (id int); SELECT 1/0;")},
	}
	if err := s.migrate(context.Background(), broken); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var absent bool
	if err := s.pool.QueryRow(context.Background(), "SELECT to_regclass('rollback_probe') IS NULL").Scan(&absent); err != nil || !absent {
		t.Fatalf("DDL not rolled back: %v %v", absent, err)
	}
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != 4 {
		t.Fatalf("failed migration recorded: %d %v", count, err)
	}
	migrate(t, s) // A failed migration does not strand the advisory lock.
	if _, err := s.pool.Exec(context.Background(), "INSERT INTO schema_migrations VALUES ('migrations/9999_future.sql', 'unknown', now())"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(context.Background()); err == nil {
		t.Fatal("old binary accepted newer schema")
	}
}

func TestLeaseMigrationBackfillsExistingJob(t *testing.T) {
	store, _ := database(t)
	admission, err := migrations.ReadFile("migrations/0001_admission.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(context.Background(), fstest.MapFS{
		"migrations/0001_admission.sql": {Data: admission},
	}); err != nil {
		t.Fatal(err)
	}
	spec, err := (jobs.Spec{Kind: "demo.checksum", Payload: "pre-lease\x00", MaxAttempts: 5}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(encoded)
	if _, err := store.pool.Exec(context.Background(), `INSERT INTO jobs
        (id, idempotency_key, request_fingerprint, spec) VALUES ($1, $2, $3, $4)`,
		strings.Repeat("b", 32), "before-leases", fingerprint[:], string(encoded)); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var maxAttempts int
	var availableAt time.Time
	if err := store.pool.QueryRow(context.Background(), `SELECT max_attempts, available_at
        FROM jobs WHERE idempotency_key = 'before-leases'`).Scan(&maxAttempts, &availableAt); err != nil {
		t.Fatal(err)
	}
	if maxAttempts != 5 || availableAt.IsZero() {
		t.Fatalf("backfill max_attempts=%d available_at=%s", maxAttempts, availableAt)
	}
}

func TestOutcomeMigrationBackfillsExistingTerminalStates(t *testing.T) {
	store, _ := database(t)
	admission, err := migrations.ReadFile("migrations/0001_admission.sql")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := migrations.ReadFile("migrations/0002_leases.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(context.Background(), fstest.MapFS{
		"migrations/0001_admission.sql": {Data: admission},
		"migrations/0002_leases.sql":    {Data: leases},
	}); err != nil {
		t.Fatal(err)
	}
	spec, err := (jobs.Spec{Kind: "demo.checksum", MaxAttempts: 3}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(encoded)
	states := []jobs.State{jobs.Succeeded, jobs.Failed, jobs.Canceled, jobs.Retrying}
	for i, state := range states {
		id := fmt.Sprintf("%032x", i+1)
		if _, err := store.pool.Exec(context.Background(), `INSERT INTO jobs
            (id, idempotency_key, request_fingerprint, spec, state, attempts, max_attempts)
            VALUES ($1, $2, $3, $4, $5, 1, 3)`,
			id, "before-outcomes-"+string(state), fingerprint[:], string(encoded), state); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		var result, message string
		var resultIsNull, messageIsNull bool
		if err := store.pool.QueryRow(context.Background(), `SELECT COALESCE(result, ''), result IS NULL,
			COALESCE(error, ''), error IS NULL FROM jobs WHERE idempotency_key = $1`,
			"before-outcomes-"+string(state)).Scan(&result, &resultIsNull, &message, &messageIsNull); err != nil {
			t.Fatal(err)
		}
		if state == jobs.Succeeded {
			if resultIsNull || result != "result unavailable before migration 0003" || !messageIsNull {
				t.Fatalf("succeeded backfill result=%q result_null=%t error_null=%t", result, resultIsNull, messageIsNull)
			}
		} else if !resultIsNull || messageIsNull || message == "" {
			t.Fatalf("%s backfill result_null=%t error=%q error_null=%t", state, resultIsNull, message, messageIsNull)
		}
	}
}

func TestConcurrentIdempotency(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict=%v", conflict), func(t *testing.T) {
			s, dsn := database(t)
			migrate(t, s)
			other, err := Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			type outcome struct {
				job    jobs.Job
				replay bool
				err    error
			}
			results := make(chan outcome, 32)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range 32 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					spec := jobs.Spec{Kind: "demo.checksum", Payload: "same"}
					if i%2 == 0 {
						spec.MaxAttempts = 3
						spec.TimeoutMS = 1000
					}
					if conflict && i%2 == 0 {
						spec.Payload = "different"
					}
					store := s
					if i%2 == 0 {
						store = other
					}
					j, replay, err := store.Submit(context.Background(), "shared-key", spec)
					results <- outcome{j, replay, err}
				}()
			}
			close(start)
			wg.Wait()
			close(results)
			created, replays, conflicts := 0, 0, 0
			var id string
			for result := range results {
				if errors.Is(result.err, jobs.ErrConflict) {
					conflicts++
					continue
				}
				if result.err != nil {
					t.Fatal(result.err)
				}
				if id == "" {
					id = result.job.ID
				}
				if id != result.job.ID {
					t.Fatal("duplicate jobs")
				}
				if result.replay {
					replays++
				} else {
					created++
				}
			}
			expectedReplays, expectedConflicts := 31, 0
			if conflict {
				expectedReplays, expectedConflicts = 15, 16
			}
			if created != 1 || replays != expectedReplays || conflicts != expectedConflicts {
				t.Fatalf("created=%d replay=%d conflict=%d", created, replays, conflicts)
			}
			var count int
			if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 1 {
				t.Fatalf("rows=%d err=%v", count, err)
			}
		})
	}
}

func TestUncommittedWinner(t *testing.T) {
	for _, commit := range []bool{true, false} {
		t.Run(fmt.Sprintf("commit=%v", commit), func(t *testing.T) {
			s, dsn := database(t)
			migrate(t, s)
			holder, err := pgx.Connect(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close(context.Background())
			tx, err := holder.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(tx)
			spec, _ := (jobs.Spec{Kind: "demo.checksum", Payload: "blocked"}).Normalize()
			encoded, _ := json.Marshal(spec)
			fingerprint := sha256.Sum256(encoded)
			winnerID := strings.Repeat("a", 32)
			if _, err := tx.Exec(context.Background(), "INSERT INTO jobs (id,idempotency_key,request_fingerprint,spec,max_attempts) VALUES ($1,$2,$3,$4,$5)", winnerID, "contended", fingerprint[:], string(encoded), spec.MaxAttempts); err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				job    jobs.Job
				replay bool
				err    error
			}
			done := make(chan outcome, 1)
			go func() { j, r, e := s.Submit(context.Background(), "contended", spec); done <- outcome{j, r, e} }()
			// Observe an actual lock wait; do not rely on a sleep to create the race.
			deadline := time.Now().Add(time.Second)
			for {
				var blocked bool
				err := s.pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
     WHERE application_name = current_setting('application_name') AND pid <> pg_backend_pid()
     AND wait_event_type = 'Lock' AND query LIKE 'INSERT INTO jobs%')`).Scan(&blocked)
				if err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("submit did not wait on uncommitted winner")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if commit {
				err = tx.Commit(context.Background())
			} else {
				err = tx.Rollback(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if result.err != nil || result.replay != commit || (result.job.ID == winnerID) != commit {
					t.Fatalf("outcome: %+v", result)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("submit remained blocked")
			}
		})
	}
}

func TestPersistenceValidationAndCancellation(t *testing.T) {
	s, dsn := database(t)
	migrate(t, s)
	spec := jobs.Spec{Kind: "demo.checksum", Payload: "hello\x00世界"}
	j, replay, err := s.Submit(context.Background(), "durable", spec)
	if err != nil || replay {
		t.Fatalf("submit: %v %v", replay, err)
	}
	s.Close() // Recreate the client/pool: no memory map can satisfy this read.
	reopened, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Get(context.Background(), j.ID)
	if err != nil || got.ID != j.ID || got.Spec != j.Spec || !got.CreatedAt.Equal(j.CreatedAt) || !got.UpdatedAt.Equal(j.UpdatedAt) || got.State != jobs.Queued || got.Attempts != 0 {
		t.Fatalf("persisted job: %+v err=%v", got, err)
	}
	again, replay, err := reopened.Submit(context.Background(), "durable", spec)
	if err != nil || !replay || again.ID != j.ID {
		t.Fatalf("reconnect replay: %+v %v %v", again, replay, err)
	}
	var fingerprint []byte
	if err := reopened.pool.QueryRow(context.Background(), "SELECT request_fingerprint FROM jobs WHERE id=$1", j.ID).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(j.Spec)
	want := sha256.Sum256(encoded)
	if string(fingerprint) != string(want[:]) {
		t.Fatal("fingerprint does not cover normalized spec")
	}
	if _, err := reopened.Get(context.Background(), "missing"); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	for _, input := range []struct {
		key  string
		spec jobs.Spec
	}{
		{"bad key", spec}, {"retry-valid", jobs.Spec{Kind: "invalid"}}, {"bad-utf8", jobs.Spec{Kind: "demo.checksum", Payload: string([]byte{0xff})}},
	} {
		if _, _, err := reopened.Submit(context.Background(), input.key, input.spec); !errors.Is(err, jobs.ErrInvalid) {
			t.Fatalf("validation: %v", err)
		}
	}
	if _, replay, err := reopened.Submit(context.Background(), "retry-valid", spec); err != nil || replay {
		t.Fatalf("invalid request consumed key: %v %v", replay, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := reopened.Submit(ctx, "canceled", spec); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %v", err)
	}
	if _, replay, err := reopened.Submit(context.Background(), "canceled", spec); err != nil || replay {
		t.Fatalf("canceled admission consumed key: %v %v", replay, err)
	}
}
