package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOpenConnectionFailureDoesNotEchoConfiguration(t *testing.T) {
	const marker = "synthetic_private_value"
	// A nonexistent Unix socket in a private temporary directory cannot reach
	// a real database. pgx still includes configured user/database in its error.
	dsn := "host=" + t.TempDir() + " user=" + marker + " dbname=" + marker + " password=" + marker + " sslmode=disable"
	store, err := Open(context.Background(), dsn)
	if store != nil {
		store.Close()
		t.Fatal("unexpected connection")
	}
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatal("connection error echoed configuration")
	}
	if err.Error() != "connect to PostgreSQL failed: check connectivity and credentials" {
		t.Fatalf("unexpected category: %v", err)
	}
}

func TestOpenPreservesContextFailureIdentity(t *testing.T) {
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		store, err := Open(ctx, "host="+t.TempDir()+" user=synthetic_private_value dbname=synthetic_private_value sslmode=disable")
		cancel()
		if store != nil {
			store.Close()
			t.Fatal("unexpected connection")
		}
		if !errors.Is(err, want) {
			t.Fatalf("got %v, want %v", err, want)
		}
		if strings.Contains(err.Error(), "synthetic_private_value") {
			t.Fatal("context error echoed configuration")
		}
	}
}
