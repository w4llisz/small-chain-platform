//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/jobs"
)

func TestListJobsUsesFilteredKeysetPagination(t *testing.T) {
	store, _ := database(t)
	migrate(t, store)

	created := make([]jobs.Job, 4)
	for i := range created {
		job, replay, err := store.Submit(context.Background(), "job-list-"+string(rune('a'+i)), jobs.Spec{
			Kind: "demo.checksum", Payload: "payload",
		})
		if err != nil || replay {
			t.Fatalf("submit %d: replay=%t err=%v", i, replay, err)
		}
		created[i] = job
	}
	for _, index := range []int{1, 3} {
		if _, err := store.Cancel(context.Background(), created[index].ID, "listed cancellation"); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for i, job := range created {
		if _, err := store.pool.Exec(context.Background(), `UPDATE jobs SET created_at = $2 WHERE id = $1`,
			job.ID, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	first, err := store.ListJobs(context.Background(), JobListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 2 || first.Jobs[0].ID != created[3].ID || first.Jobs[1].ID != created[2].ID || first.Next == nil {
		t.Fatalf("first page: %+v", first)
	}
	if first.Jobs[0].Kind != "demo.checksum" || first.Jobs[0].MaxAttempts != 3 || first.Jobs[0].Error == "" {
		t.Fatalf("summary fields: %+v", first.Jobs[0])
	}

	newer, replay, err := store.Submit(context.Background(), "job-list-newer", jobs.Spec{Kind: "demo.checksum"})
	if err != nil || replay {
		t.Fatalf("concurrent newer submit: replay=%t err=%v", replay, err)
	}
	second, err := store.ListJobs(context.Background(), JobListOptions{Before: first.Next, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Jobs) != 2 || second.Jobs[0].ID != created[1].ID || second.Jobs[1].ID != created[0].ID || second.Next != nil {
		t.Fatalf("second page: %+v", second)
	}
	for _, summary := range second.Jobs {
		if summary.ID == newer.ID {
			t.Fatal("newer insert shifted a later keyset page")
		}
	}

	canceled, err := store.ListJobs(context.Background(), JobListOptions{State: jobs.Canceled, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(canceled.Jobs) != 2 || canceled.Jobs[0].ID != created[3].ID || canceled.Jobs[1].ID != created[1].ID || canceled.Next != nil {
		t.Fatalf("canceled page: %+v", canceled)
	}
	empty, err := store.ListJobs(context.Background(), JobListOptions{State: jobs.Failed, Limit: 10})
	if err != nil || empty.Jobs == nil || len(empty.Jobs) != 0 || empty.Next != nil {
		t.Fatalf("empty page: %+v err=%v", empty, err)
	}

	for _, options := range []JobListOptions{
		{Limit: 0},
		{Limit: MaxJobPageSize + 1},
		{State: jobs.State("unknown"), Limit: 1},
		{Before: &JobPosition{ID: created[0].ID}, Limit: 1},
		{Before: &JobPosition{CreatedAt: time.Now(), ID: "bad"}, Limit: 1},
	} {
		if _, err := store.ListJobs(context.Background(), options); !errors.Is(err, jobs.ErrInvalid) {
			t.Fatalf("options=%+v err=%v", options, err)
		}
	}

	for _, expected := range []struct {
		name, fragment string
	}{
		{"jobs_recent_idx", "created_at DESC, id DESC"},
		{"jobs_state_recent_idx", "state, created_at DESC, id DESC"},
	} {
		var definition string
		if err := store.pool.QueryRow(context.Background(), `SELECT indexdef FROM pg_indexes WHERE indexname = $1`, expected.name).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(definition, expected.fragment) {
			t.Fatalf("%s: %s", expected.name, definition)
		}
	}
}
