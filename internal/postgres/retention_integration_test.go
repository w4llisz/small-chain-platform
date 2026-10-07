//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

func submitRetentionJob(t *testing.T, store *Store, key string, spec jobs.Spec) jobs.Job {
	t.Helper()
	job, replay, err := store.Submit(context.Background(), key, spec)
	if err != nil || replay {
		t.Fatalf("submit %s: replay=%t err=%v", key, replay, err)
	}
	return job
}

func TestAdmissionCapacityKeepsReplaysAvailable(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	policy, err := store.ConfigureAdmission(context.Background(), 2, time.Hour)
	if err != nil || policy.MaxJobs != 2 || policy.ReplayWindow != time.Hour {
		t.Fatalf("configure: %+v %v", policy, err)
	}

	first, replay, err := store.Submit(context.Background(), "capacity-first", jobs.Spec{Kind: "demo.checksum", Payload: "first"})
	if err != nil || replay {
		t.Fatalf("first submit: replay=%t err=%v", replay, err)
	}
	if _, replay, err := store.Submit(context.Background(), "capacity-second", jobs.Spec{Kind: "demo.checksum"}); err != nil || replay {
		t.Fatalf("second submit: replay=%t err=%v", replay, err)
	}
	if _, _, err := store.Submit(context.Background(), "capacity-third", jobs.Spec{Kind: "demo.checksum"}); !errors.Is(err, jobs.ErrCapacity) {
		t.Fatalf("new key above capacity: %v", err)
	}

	again, replay, err := store.Submit(context.Background(), "capacity-first", jobs.Spec{Kind: "demo.checksum", Payload: "first"})
	if err != nil || !replay || again.ID != first.ID {
		t.Fatalf("replay at capacity: %+v replay=%t err=%v", again, replay, err)
	}
	if _, _, err := store.Submit(context.Background(), "capacity-first", jobs.Spec{Kind: "demo.checksum", Payload: "changed"}); !errors.Is(err, jobs.ErrConflict) {
		t.Fatalf("conflict at capacity: %v", err)
	}
	policy, err = store.AdmissionPolicy(context.Background())
	if err != nil || policy.RetainedJobs != 2 {
		t.Fatalf("policy after admission: %+v %v", policy, err)
	}
	if _, err := store.ConfigureAdmission(context.Background(), 1, time.Hour); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("lower cap below retained count: %v", err)
	}
}

func TestPurgeTerminalHonorsReplayWindowAndActiveSafety(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	if _, err := store.ConfigureAdmission(context.Background(), 4, time.Hour); err != nil {
		t.Fatal(err)
	}

	oldTerminal := submitRetentionJob(t, store, "old-terminal", jobs.Spec{Kind: "demo.checksum"})
	if _, err := store.Cancel(context.Background(), oldTerminal.ID, "finished for retention test"); err != nil {
		t.Fatal(err)
	}
	recentTerminal := submitRetentionJob(t, store, "recent-terminal", jobs.Spec{Kind: "demo.checksum"})
	if _, err := store.Cancel(context.Background(), recentTerminal.ID, "recent terminal"); err != nil {
		t.Fatal(err)
	}
	oldActive := submitRetentionJob(t, store, "old-active", jobs.Spec{Kind: "demo.checksum"})
	if _, err := store.pool.Exec(context.Background(), `UPDATE jobs
SET updated_at = statement_timestamp() - interval '2 hours'
WHERE id IN ($1, $2)`, oldTerminal.ID, oldActive.ID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.Submit(context.Background(), "fills-cap", jobs.Spec{Kind: "demo.checksum"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Submit(context.Background(), "blocked-before-purge", jobs.Spec{Kind: "demo.checksum"}); !errors.Is(err, jobs.ErrCapacity) {
		t.Fatalf("capacity before purge: %v", err)
	}
	deleted, err := store.PurgeTerminal(context.Background(), 10)
	if err != nil || deleted != 1 {
		t.Fatalf("purge: deleted=%d err=%v", deleted, err)
	}
	if _, err := store.Get(context.Background(), oldTerminal.ID); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("old terminal retained: %v", err)
	}
	for _, id := range []string{recentTerminal.ID, oldActive.ID} {
		if _, err := store.Get(context.Background(), id); err != nil {
			t.Fatalf("eligible filter removed %s: %v", id, err)
		}
	}
	if _, replay, err := store.Submit(context.Background(), "old-terminal", jobs.Spec{Kind: "demo.checksum"}); err != nil || replay {
		t.Fatalf("expired key was not reusable: replay=%t err=%v", replay, err)
	}
	policy, err := store.AdmissionPolicy(context.Background())
	if err != nil || policy.RetainedJobs != 4 {
		t.Fatalf("policy after purge/reuse: %+v %v", policy, err)
	}
	var indexDefinition string
	if err := store.pool.QueryRow(context.Background(), `SELECT indexdef FROM pg_indexes
WHERE indexname = 'jobs_terminal_retention_idx'`).Scan(&indexDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexDefinition, "updated_at, id") || !strings.Contains(indexDefinition, "succeeded") || !strings.Contains(indexDefinition, "failed") || !strings.Contains(indexDefinition, "canceled") {
		t.Fatalf("unexpected retention index: %s", indexDefinition)
	}
}

func TestConcurrentAdmissionAndPurgeStayWithinCapacity(t *testing.T) {
	store, dsn := database(t)
	migrate(t, store)
	other, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := store.ConfigureAdmission(context.Background(), 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	old := submitRetentionJob(t, store, "race-old", jobs.Spec{Kind: "demo.checksum"})
	if _, err := store.Cancel(context.Background(), old.ID, "eligible terminal"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(context.Background(), `UPDATE jobs
SET updated_at = statement_timestamp() - interval '2 minutes' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	type submitResult struct {
		job jobs.Job
		err error
	}
	results := make(chan submitResult, 16)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s := store
			if i%2 == 0 {
				s = other
			}
			job, _, err := s.Submit(context.Background(), fmt.Sprintf("race-new-%02d", i), jobs.Spec{Kind: "demo.checksum"})
			results <- submitResult{job: job, err: err}
		}()
	}
	purgeDone := make(chan error, 1)
	go func() {
		<-start
		_, err := other.PurgeTerminal(context.Background(), 1)
		purgeDone <- err
	}()
	close(start)
	wg.Wait()
	close(results)
	if err := <-purgeDone; err != nil {
		t.Fatal(err)
	}

	succeeded := 0
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, jobs.ErrCapacity):
		default:
			t.Fatalf("unexpected submit result: %+v", result)
		}
	}
	if succeeded > 1 {
		t.Fatalf("capacity overshot: %d submissions succeeded", succeeded)
	}
	var actual int
	if err := store.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs`).Scan(&actual); err != nil {
		t.Fatal(err)
	}
	policy, err := store.AdmissionPolicy(context.Background())
	if err != nil || policy.RetainedJobs != actual || actual > policy.MaxJobs {
		t.Fatalf("counter/cap mismatch: policy=%+v actual=%d err=%v", policy, actual, err)
	}
}

func TestRetentionInputBounds(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	for _, limit := range []int{0, maxPurgeBatch + 1} {
		if _, err := store.PurgeTerminal(context.Background(), limit); !errors.Is(err, jobs.ErrInvalid) {
			t.Fatalf("purge limit %d: %v", limit, err)
		}
	}
	for _, tc := range []struct {
		maxJobs int
		window  time.Duration
	}{
		{0, time.Hour},
		{maxAdmissionCapacity + 1, time.Hour},
		{1, minReplayWindow - time.Nanosecond},
		{1, maxReplayWindow + time.Nanosecond},
	} {
		if _, err := store.ConfigureAdmission(context.Background(), tc.maxJobs, tc.window); !errors.Is(err, jobs.ErrInvalid) {
			t.Fatalf("configure %+v: %v", tc, err)
		}
	}
}
