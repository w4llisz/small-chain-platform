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

func submitAndClaim(t *testing.T, store *Store, key string, spec jobs.Spec) Claim {
	t.Helper()
	if _, _, err := store.Submit(context.Background(), key, spec); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(context.Background(), "worker-"+key, 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim %s: %+v, %v", key, claims, err)
	}
	return claims[0]
}

func TestHeartbeatAndCompletion(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	claim := submitAndClaim(t, store, "complete", jobs.Spec{Kind: "demo.checksum", Payload: "complete"})
	wrongOwner := claim
	wrongOwner.Owner = "worker-other"
	if _, err := store.Heartbeat(context.Background(), wrongOwner, time.Minute); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("wrong-owner heartbeat: %v", err)
	}
	wrongVersion := claim
	wrongVersion.Version++
	if _, err := store.Complete(context.Background(), wrongVersion, "wrong-version"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("wrong-version completion: %v", err)
	}

	renewed, err := store.Heartbeat(context.Background(), claim, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Version != claim.Version || renewed.Owner != claim.Owner || renewed.ExpiresAt.Sub(renewed.Job.UpdatedAt) != 45*time.Second {
		t.Fatalf("renewed claim: %+v", renewed)
	}
	completed, err := store.Complete(context.Background(), renewed, "checksum-result")
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != jobs.Succeeded || completed.Result != "checksum-result" || completed.Error != "" {
		t.Fatalf("completed job: %+v", completed)
	}
	got, err := store.Get(context.Background(), claim.Job.ID)
	if err != nil || got != completed {
		t.Fatalf("persisted completion: %+v, %v", got, err)
	}
	if _, err := store.Heartbeat(context.Background(), renewed, time.Minute); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("heartbeat after completion: %v", err)
	}
	if _, err := store.Complete(context.Background(), renewed, "late"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("second completion: %v", err)
	}
}

func TestFailAttemptRetriesThenExhausts(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	first := submitAndClaim(t, store, "retry", jobs.Spec{Kind: "demo.checksum", Payload: "retry", MaxAttempts: 2})

	retrying, err := store.FailAttempt(context.Background(), first, "temporary failure", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if retrying.State != jobs.Retrying || retrying.Attempts != 1 || retrying.Error != "temporary failure" {
		t.Fatalf("retrying job: %+v", retrying)
	}
	if claims, err := store.ClaimDue(context.Background(), "worker-early", 1, time.Minute); err != nil || len(claims) != 0 {
		t.Fatalf("delayed retry was claimable: %+v, %v", claims, err)
	}
	if _, err := store.Complete(context.Background(), first, "late-first-result"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("released first lease completed: %v", err)
	}
	if _, err := store.pool.Exec(context.Background(), `UPDATE jobs
        SET available_at = statement_timestamp() - interval '1 second' WHERE id = $1`, first.Job.ID); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(context.Background(), "worker-retry", 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("second claim: %+v, %v", claims, err)
	}
	second := claims[0]
	if second.Job.Attempts != 2 || second.Version != first.Version+1 || second.Job.Error != "" {
		t.Fatalf("second claim token: %+v", second)
	}
	if _, err := store.Heartbeat(context.Background(), first, time.Minute); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("old version heartbeat: %v", err)
	}
	failed, err := store.FailAttempt(context.Background(), second, "permanent after budget", 0)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != jobs.Failed || failed.Attempts != 2 || failed.Error != "permanent after budget" {
		t.Fatalf("failed job: %+v", failed)
	}
	if claims, err := store.ClaimDue(context.Background(), "worker-third", 1, time.Minute); err != nil || len(claims) != 0 {
		t.Fatalf("failed job reclaimed: %+v, %v", claims, err)
	}
}

func TestExpiredLeaseCannotMutate(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	claim := submitAndClaim(t, store, "expired", jobs.Spec{Kind: "demo.checksum", Payload: "expired"})
	if _, err := store.pool.Exec(context.Background(), `UPDATE jobs
        SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, claim.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(context.Background(), claim, time.Minute); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired heartbeat: %v", err)
	}
	if _, err := store.Complete(context.Background(), claim, "late"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired completion: %v", err)
	}
	if _, err := store.FailAttempt(context.Background(), claim, "late failure", 0); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired failure: %v", err)
	}
	got, err := store.Get(context.Background(), claim.Job.ID)
	if err != nil || got.State != jobs.Running {
		t.Fatalf("expired lease state changed: %+v, %v", got, err)
	}
}

func TestCancelFencesRunningLeaseAndIsIdempotent(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	claim := submitAndClaim(t, store, "cancel", jobs.Spec{Kind: "demo.checksum", Payload: "cancel"})

	canceled, err := store.Cancel(context.Background(), claim.Job.ID, "operator canceled")
	if err != nil {
		t.Fatal(err)
	}
	if canceled.State != jobs.Canceled || canceled.Error != "operator canceled" {
		t.Fatalf("canceled job: %+v", canceled)
	}
	var version int64
	var ownerIsNull, expiresIsNull bool
	if err := store.pool.QueryRow(context.Background(), `SELECT lease_version,
		lease_owner IS NULL, lease_expires_at IS NULL FROM jobs WHERE id = $1`, claim.Job.ID).
		Scan(&version, &ownerIsNull, &expiresIsNull); err != nil {
		t.Fatal(err)
	}
	if version != claim.Version+1 || !ownerIsNull || !expiresIsNull {
		t.Fatalf("cancel fence version=%d owner_null=%t expires_null=%t", version, ownerIsNull, expiresIsNull)
	}
	if _, err := store.Complete(context.Background(), claim, "late"); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("canceled lease completed: %v", err)
	}
	again, err := store.Cancel(context.Background(), claim.Job.ID, "different reason ignored")
	if err != nil || again != canceled {
		t.Fatalf("idempotent cancel: %+v, %v", again, err)
	}

	queued, _, err := store.Submit(context.Background(), "cancel-queued", jobs.Spec{Kind: "demo.checksum"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.Cancel(context.Background(), queued.ID, "queue cleanup"); err != nil || got.State != jobs.Canceled {
		t.Fatalf("queued cancel: %+v, %v", got, err)
	}
	completed := submitAndClaim(t, store, "cancel-terminal", jobs.Spec{Kind: "demo.checksum"})
	if _, err := store.Complete(context.Background(), completed, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(context.Background(), completed.Job.ID, "too late"); !errors.Is(err, jobs.ErrTerminal) {
		t.Fatalf("terminal cancel: %v", err)
	}
}

func TestCompletionCancelRaceHasOneTerminalWinner(t *testing.T) {
	store, dsn := database(t)
	migrate(t, store)
	other, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	claim := submitAndClaim(t, store, "terminal-race", jobs.Spec{Kind: "demo.checksum"})

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := store.Complete(context.Background(), claim, "winner-result")
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := other.Cancel(context.Background(), claim.Job.ID, "winner-cancel")
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrStaleLease) && !errors.Is(err, jobs.ErrTerminal) {
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("terminal race successes=%d, want 1", successes)
	}
	got, err := store.Get(context.Background(), claim.Job.ID)
	if err != nil || (got.State != jobs.Succeeded && got.State != jobs.Canceled) {
		t.Fatalf("terminal race result: %+v, %v", got, err)
	}
}

func TestLifecycleValidationAndCancellation(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)
	valid := Claim{Job: jobs.Job{ID: strings.Repeat("a", 32)}, Owner: "worker", Version: 1}
	invalidClaims := []Claim{
		{Job: jobs.Job{ID: "bad"}, Owner: "worker", Version: 1},
		{Job: jobs.Job{ID: strings.Repeat("a", 32)}, Owner: "bad owner", Version: 1},
		{Job: jobs.Job{ID: strings.Repeat("a", 32)}, Owner: "worker", Version: 0},
	}
	for _, claim := range invalidClaims {
		if _, err := store.Heartbeat(context.Background(), claim, time.Minute); !errors.Is(err, jobs.ErrInvalid) {
			t.Fatalf("invalid claim: %v", err)
		}
	}
	if _, err := store.Heartbeat(context.Background(), valid, minLease-time.Nanosecond); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("short heartbeat: %v", err)
	}
	if _, err := store.Complete(context.Background(), valid, string([]byte{0xff})); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("invalid result: %v", err)
	}
	if _, err := store.Complete(context.Background(), valid, strings.Repeat("x", maxResultBytes+1)); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("large result: %v", err)
	}
	if _, err := store.FailAttempt(context.Background(), valid, "", 0); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("empty failure: %v", err)
	}
	if _, err := store.FailAttempt(context.Background(), valid, "failure", -time.Nanosecond); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("negative retry: %v", err)
	}
	if _, err := store.Cancel(context.Background(), "bad", "reason"); !errors.Is(err, jobs.ErrInvalid) {
		t.Fatalf("invalid cancel ID: %v", err)
	}
	if _, err := store.Cancel(context.Background(), strings.Repeat("b", 32), "reason"); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("missing cancel: %v", err)
	}

	claim := submitAndClaim(t, store, "canceled-context", jobs.Spec{Kind: "demo.checksum"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Complete(ctx, claim, "ignored"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled completion: %v", err)
	}
}
