//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

func expireLease(t *testing.T, store *Store, id string) {
	t.Helper()
	if _, err := store.pool.Exec(context.Background(), `UPDATE jobs
        SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverExpiredRetriesThenExhaustsAndFences(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	first := submitAndClaim(t, store, "recover-budget", jobs.Spec{Kind: "demo.checksum", MaxAttempts: 2})

	if recovered, err := store.RecoverExpired(context.Background(), 1, time.Hour); err != nil || len(recovered) != 0 {
		t.Fatalf("live lease recovered: %+v, %v", recovered, err)
	}
	expireLease(t, store, first.Job.ID)
	recovered, err := store.RecoverExpired(context.Background(), 1, time.Hour)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("first recovery: %+v, %v", recovered, err)
	}
	if recovered[0].State != jobs.Retrying || recovered[0].Attempts != 1 || recovered[0].Error != leaseExpiredMessage {
		t.Fatalf("retry recovery: %+v", recovered[0])
	}
	var version int64
	var ownerIsNull, expiryIsNull bool
	var retryDelayNanos int64
	if err := store.pool.QueryRow(context.Background(), `SELECT lease_version,
        lease_owner IS NULL, lease_expires_at IS NULL,
        (EXTRACT(EPOCH FROM (available_at - updated_at)) * 1000000000)::bigint
        FROM jobs WHERE id = $1`, first.Job.ID).
		Scan(&version, &ownerIsNull, &expiryIsNull, &retryDelayNanos); err != nil {
		t.Fatal(err)
	}
	retryDelay := time.Duration(retryDelayNanos)
	if version != first.Version+1 || !ownerIsNull || !expiryIsNull || retryDelay != time.Hour {
		t.Fatalf("recovery fence version=%d owner_null=%t expiry_null=%t delay=%s", version, ownerIsNull, expiryIsNull, retryDelay)
	}
	if _, err := store.Complete(context.Background(), first, "late"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale worker completed recovered job: %v", err)
	}
	if claims, err := store.ClaimDue(context.Background(), "worker-early", 1, time.Minute); err != nil || len(claims) != 0 {
		t.Fatalf("recovery delay ignored: %+v, %v", claims, err)
	}
	if _, err := store.pool.Exec(context.Background(), `UPDATE jobs
        SET available_at = statement_timestamp() - interval '1 second' WHERE id = $1`, first.Job.ID); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(context.Background(), "worker-recovered", 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("reclaim after recovery: %+v, %v", claims, err)
	}
	second := claims[0]
	if second.Job.Attempts != 2 || second.Version != first.Version+2 {
		t.Fatalf("second claim: %+v", second)
	}
	expireLease(t, store, second.Job.ID)
	recovered, err = store.RecoverExpired(context.Background(), 1, 0)
	if err != nil || len(recovered) != 1 || recovered[0].State != jobs.Failed || recovered[0].Attempts != 2 {
		t.Fatalf("exhausted recovery: %+v, %v", recovered, err)
	}
	if claims, err := store.ClaimDue(context.Background(), "worker-after-budget", 1, time.Minute); err != nil || len(claims) != 0 {
		t.Fatalf("exhausted job reclaimed: %+v, %v", claims, err)
	}
}

func TestRecoverExpiredConcurrentSweepersRecoverEachJobOnce(t *testing.T) {
	store, dsn := database(t)
	migrate(t, store)
	other, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	const total = 24
	for i := range total {
		key := "recover-concurrent-" + string(rune('a'+i))
		if _, _, err := store.Submit(context.Background(), key, jobs.Spec{Kind: "demo.checksum", MaxAttempts: 2}); err != nil {
			t.Fatal(err)
		}
	}
	claims, err := store.ClaimDue(context.Background(), "worker-crashed", total, time.Minute)
	if err != nil || len(claims) != total {
		t.Fatalf("claims: %d, %v", len(claims), err)
	}
	if _, err := store.pool.Exec(context.Background(), `UPDATE jobs
        SET lease_expires_at = statement_timestamp() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}

	type result struct {
		jobs []jobs.Job
		err  error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, sweeper := range []*Store{store, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			recovered, err := sweeper.RecoverExpired(context.Background(), total/2, 0)
			results <- result{recovered, err}
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
		if len(result.jobs) != total/2 {
			t.Fatalf("sweeper recovered %d jobs, want %d", len(result.jobs), total/2)
		}
		for _, job := range result.jobs {
			if _, duplicate := seen[job.ID]; duplicate {
				t.Fatalf("job %s recovered twice", job.ID)
			}
			seen[job.ID] = struct{}{}
			if job.State != jobs.Retrying || job.Error != leaseExpiredMessage {
				t.Fatalf("recovered job: %+v", job)
			}
		}
	}
	if len(seen) != total {
		t.Fatalf("unique recovered jobs=%d, want %d", len(seen), total)
	}
	if recovered, err := store.RecoverExpired(context.Background(), total, 0); err != nil || len(recovered) != 0 {
		t.Fatalf("jobs recovered twice: %+v, %v", recovered, err)
	}
	for _, claim := range claims {
		if _, err := store.Complete(context.Background(), claim, "late"); !errors.Is(err, ErrStaleLease) {
			t.Fatalf("stale claim %s completed: %v", claim.Job.ID, err)
		}
	}
}

func TestRecoverExpiredIndexValidationAndCancellation(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)

	var definition string
	if err := store.pool.QueryRow(context.Background(), `SELECT indexdef FROM pg_indexes
        WHERE indexname = 'jobs_expired_lease_idx'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(definition, "lease_expires_at, id") || !strings.Contains(definition, "state = 'running'") {
		t.Fatalf("unexpected recovery index: %s", definition)
	}
	for _, test := range []struct {
		limit int
		delay time.Duration
	}{
		{0, 0},
		{maxClaimBatch + 1, 0},
		{1, -time.Nanosecond},
		{1, maxRetryDelay + time.Nanosecond},
	} {
		if _, err := store.RecoverExpired(context.Background(), test.limit, test.delay); !errors.Is(err, jobs.ErrInvalid) {
			t.Fatalf("limit=%d delay=%s: %v", test.limit, test.delay, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.RecoverExpired(ctx, 1, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled recovery: %v", err)
	}
}
