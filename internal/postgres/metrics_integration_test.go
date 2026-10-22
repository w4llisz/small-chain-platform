//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

func TestCountJobsByStateReadsDurableSnapshot(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)

	queued, _, err := store.Submit(context.Background(), "metrics-queued", jobs.Spec{Kind: "demo.checksum"})
	if err != nil {
		t.Fatal(err)
	}
	canceled, _, err := store.Submit(context.Background(), "metrics-canceled", jobs.Spec{Kind: "demo.checksum"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Cancel(context.Background(), canceled.ID, "metrics fixture"); err != nil {
		t.Fatal(err)
	}

	counts, err := store.CountJobsByState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[jobs.State]int64{jobs.Queued: 1, jobs.Canceled: 1}
	for _, state := range []jobs.State{jobs.Queued, jobs.Running, jobs.Retrying, jobs.Succeeded, jobs.Failed, jobs.Canceled} {
		if counts[state] != want[state] {
			t.Fatalf("state %s: got %d, want %d (queued=%s)", state, counts[state], want[state], queued.ID)
		}
	}
}
