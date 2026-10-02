//go:build integration

package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

func TestClaimDueConcurrent(t *testing.T) {
	store, dsn := database(t)
	migrate(t, store)
	other, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	const total = 24
	for i := range total {
		key := "claim-concurrent-" + string(rune('a'+i))
		if _, _, err := store.Submit(context.Background(), key, jobs.Spec{Kind: "demo.checksum", Payload: key}); err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		claims []Claim
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, claimant := range []*Store{store, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, err := claimant.ClaimDue(context.Background(), "worker-"+string(rune('a'+i)), total/2, 30*time.Second)
			results <- result{claimed, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	seen := make(map[string]struct{}, total)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.claims) != total/2 {
			t.Fatalf("claimed %d jobs, want %d", len(result.claims), total/2)
		}
		for _, claim := range result.claims {
			if _, exists := seen[claim.Job.ID]; exists {
				t.Fatalf("job %s claimed twice", claim.Job.ID)
			}
			seen[claim.Job.ID] = struct{}{}
			if claim.Job.State != jobs.Running || claim.Job.Attempts != 1 || claim.Version != 1 || !strings.HasPrefix(claim.Owner, "worker-") {
				t.Fatalf("invalid claim: %+v", claim)
			}
			if got := claim.ExpiresAt.Sub(claim.Job.UpdatedAt); got != 30*time.Second {
				t.Fatalf("lease duration=%s, want 30s", got)
			}
		}
	}
	if len(seen) != total {
		t.Fatalf("unique claimed jobs=%d, want %d", len(seen), total)
	}
	claimed, err := store.ClaimDue(context.Background(), "worker-c", total, 30*time.Second)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("running jobs reclaimed: %d, %v", len(claimed), err)
	}
	if _, err := store.pool.Exec(context.Background(), "UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimDue(context.Background(), "worker-c", total, 30*time.Second)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("expired running jobs reclaimed before recovery exists: %d, %v", len(claimed), err)
	}
}

func TestClaimDueEligibilityOrderAndVersion(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)

	ids := make(map[string]string)
	for _, key := range []string{"first", "retry", "delayed", "exhausted"} {
		job, _, err := store.Submit(context.Background(), key, jobs.Spec{Kind: "demo.checksum", Payload: key})
		if err != nil {
			t.Fatal(err)
		}
		ids[key] = job.ID
	}
	updates := []struct {
		query string
		id    string
	}{
		{"UPDATE jobs SET available_at = statement_timestamp() - interval '4 seconds' WHERE id = $1", ids["first"]},
		{`UPDATE jobs SET state = 'retrying', attempts = 1, lease_version = 3,
                  available_at = statement_timestamp() - interval '3 seconds' WHERE id = $1`, ids["retry"]},
		{"UPDATE jobs SET available_at = statement_timestamp() + interval '1 hour' WHERE id = $1", ids["delayed"]},
		{`UPDATE jobs SET state = 'retrying', attempts = max_attempts,
                  available_at = statement_timestamp() - interval '5 seconds' WHERE id = $1`, ids["exhausted"]},
	}
	for _, update := range updates {
		if _, err := store.pool.Exec(context.Background(), update.query, update.id); err != nil {
			t.Fatal(err)
		}
	}

	claims, err := store.ClaimDue(context.Background(), "ordered-worker", 1, time.Minute)
	if err != nil || len(claims) != 1 || claims[0].Job.ID != ids["first"] || claims[0].Job.Attempts != 1 || claims[0].Version != 1 {
		t.Fatalf("first claim: %+v, %v", claims, err)
	}
	claims, err = store.ClaimDue(context.Background(), "ordered-worker", 10, time.Minute)
	if err != nil || len(claims) != 1 || claims[0].Job.ID != ids["retry"] || claims[0].Job.Attempts != 2 || claims[0].Version != 4 {
		t.Fatalf("retry claim: %+v, %v", claims, err)
	}
	claims, err = store.ClaimDue(context.Background(), "ordered-worker", 10, time.Minute)
	if err != nil || len(claims) != 0 {
		t.Fatalf("ineligible jobs claimed: %+v, %v", claims, err)
	}

	var indexDefinition string
	if err := store.pool.QueryRow(context.Background(), `SELECT indexdef FROM pg_indexes WHERE indexname = 'jobs_ready_idx'`).Scan(&indexDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexDefinition, "available_at, id") || !strings.Contains(indexDefinition, "queued") || !strings.Contains(indexDefinition, "retrying") || !strings.Contains(indexDefinition, "attempts < max_attempts") {
		t.Fatalf("unexpected ready index: %s", indexDefinition)
	}
}

func TestClaimDueSkipsLockedRow(t *testing.T) {
	store, dsn := database(t)
	migrate(t, store)

	var ids []string
	for _, key := range []string{"locked-a", "locked-b"} {
		job, _, err := store.Submit(context.Background(), key, jobs.Spec{Kind: "demo.checksum", Payload: key})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
	}
	sort.Strings(ids)
	if _, err := store.pool.Exec(context.Background(), "UPDATE jobs SET available_at = '2026-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
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
	var lockedID string
	if err := tx.QueryRow(context.Background(), "SELECT id FROM jobs WHERE id = $1 FOR UPDATE", ids[0]).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}

	claims, err := store.ClaimDue(context.Background(), "skip-worker", 1, time.Minute)
	if err != nil || len(claims) != 1 || claims[0].Job.ID != ids[1] {
		t.Fatalf("claim did not skip locked first row: %+v, %v", claims, err)
	}
	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	claims, err = store.ClaimDue(context.Background(), "skip-worker", 1, time.Minute)
	if err != nil || len(claims) != 1 || claims[0].Job.ID != ids[0] {
		t.Fatalf("unlocked row not claimed: %+v, %v", claims, err)
	}
}

func TestClaimDueValidationAndCancellation(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)

	for _, test := range []struct {
		name  string
		owner string
		limit int
		lease time.Duration
	}{
		{"owner", "bad owner", 1, time.Minute},
		{"zero limit", "worker", 0, time.Minute},
		{"large limit", "worker", maxClaimBatch + 1, time.Minute},
		{"short lease", "worker", 1, minLease - time.Nanosecond},
		{"long lease", "worker", 1, maxLease + time.Nanosecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.ClaimDue(context.Background(), test.owner, test.limit, test.lease); !errors.Is(err, jobs.ErrInvalid) {
				t.Fatalf("error=%v, want ErrInvalid", err)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ClaimDue(ctx, "worker", 1, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}
