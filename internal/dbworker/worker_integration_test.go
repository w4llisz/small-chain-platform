//go:build integration

package dbworker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

func TestPostgresWorkerRenewsRetriesAndRecovers(t *testing.T) {
	store, conn := workerDatabase(t)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	recovered, _, err := store.Submit(context.Background(), "worker-recovered", jobs.Spec{
		Kind: "demo.checksum", Payload: "recovered", TimeoutMS: 1_000, MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(context.Background(), "crashed-worker", 1, time.Second)
	if err != nil || len(claims) != 1 || claims[0].Job.ID != recovered.ID {
		t.Fatalf("crash setup claim: %+v, %v", claims, err)
	}
	if _, err := conn.Exec(context.Background(), `UPDATE jobs
		SET lease_expires_at = statement_timestamp() - interval '1 second'
		WHERE id = $1`, claims[0].Job.ID); err != nil {
		t.Fatal(err)
	}
	long, _, err := store.Submit(context.Background(), "worker-long", jobs.Spec{
		Kind: "demo.checksum", Payload: "long", DelayMS: 1_300, TimeoutMS: 3_000, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, _, err := store.Submit(context.Background(), "worker-retry", jobs.Spec{
		Kind: "demo.checksum", Payload: "retry", FailFirstAttempts: 1, TimeoutMS: 1_000, MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig("integration-worker")
	cfg.Concurrency = 3
	cfg.PollInterval = 10 * time.Millisecond
	cfg.LeaseDuration = time.Second
	cfg.HeartbeatInterval = 200 * time.Millisecond
	cfg.RetryBase = 10 * time.Millisecond
	cfg.RetryMax = 20 * time.Millisecond
	cfg.RecoveryInterval = 50 * time.Millisecond
	cfg.RecoveryBatch = 10
	worker, err := New(cfg, store, jobs.Checksum, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := runWorker(worker, ctx)

	wantAttempts := map[string]int{long.ID: 1, retry.ID: 2, recovered.ID: 2}
	deadline := time.Now().Add(8 * time.Second)
	for {
		allSucceeded := true
		for id, attempts := range wantAttempts {
			job, err := store.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if job.State != jobs.Succeeded || job.Attempts != attempts || job.Result == "" {
				allSucceeded = false
			}
		}
		if allSucceeded {
			break
		}
		if time.Now().After(deadline) {
			for id := range wantAttempts {
				job, _ := store.Get(context.Background(), id)
				t.Logf("job at timeout: %+v", job)
			}
			t.Fatal("worker did not reach expected terminal states")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	waitRun(t, done)
}

// Every integration test owns a disposable schema; it never truncates shared data.
func workerDatabase(t *testing.T) (*postgres.Store, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("sc_worker_%d", time.Now().UnixNano())
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
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	u.RawQuery = query.Encode()
	store, err := postgres.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return store, conn
}
