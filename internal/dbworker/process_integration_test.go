//go:build integration

package dbworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

func TestWorkerProcessRecoversAfterSIGKILL(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process recovery contract uses Linux SIGKILL/SIGTERM semantics")
	}
	binary := os.Getenv("SMALL_CHAIN_WORKER_BINARY")
	if binary == "" {
		t.Fatal("SMALL_CHAIN_WORKER_BINARY is required for process integration tests")
	}
	if info, err := os.Stat(binary); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("worker binary is not executable: %s: %v", binary, err)
	}
	store, conn, dsn := workerDatabase(t)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, _, err := store.Submit(context.Background(), "process-recovery", jobs.Spec{
		Kind: "demo.checksum", Payload: "recover after SIGKILL",
		DelayMS: 2_500, TimeoutMS: 5_000, MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	first := startWorkerProcess(t, binary, dsn, "process-worker-a")
	stale := waitForProcessClaim(t, store, conn, job.ID, "process-worker-a")
	second := startWorkerProcess(t, binary, dsn, "process-worker-b")
	first.kill(t)

	completed := waitForTerminalJob(t, store, job.ID, 10*time.Second)
	digest := sha256.Sum256([]byte("recover after SIGKILL"))
	wantResult := hex.EncodeToString(digest[:])
	if completed.State != jobs.Succeeded || completed.Attempts != 2 || completed.Result != wantResult {
		t.Fatalf("recovered job: %+v", completed)
	}
	var version int64
	if err := conn.QueryRow(context.Background(), `SELECT lease_version FROM jobs WHERE id = $1`, job.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("lease version = %d, want 3 (claim, recovery fence, reclaim)", version)
	}
	if _, err := store.Complete(context.Background(), stale, "stale result"); !errors.Is(err, postgres.ErrStaleLease) {
		t.Fatalf("stale process completion = %v, want ErrStaleLease", err)
	}
	second.stop(t)
}

type workerProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	output  lockedBuffer
	waitMu  sync.Mutex
	waitErr error
}

type lockedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}

func startWorkerProcess(t *testing.T, binary, dsn, owner string) *workerProcess {
	t.Helper()
	process := &workerProcess{done: make(chan struct{})}
	process.cmd = exec.Command(binary,
		"-owner="+owner,
		"-concurrency=1",
		"-poll-interval=20ms",
		"-lease-duration=1s",
		"-heartbeat-interval=200ms",
		"-retry-base=50ms",
		"-retry-max=50ms",
		"-recovery-interval=50ms",
		"-recovery-batch=10",
	)
	process.cmd.Env = environmentWith("DATABASE_URL", dsn)
	process.cmd.Stdout = &process.output
	process.cmd.Stderr = &process.output
	if err := process.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		err := process.cmd.Wait()
		process.waitMu.Lock()
		process.waitErr = err
		process.waitMu.Unlock()
		close(process.done)
	}()
	t.Cleanup(func() {
		select {
		case <-process.done:
			return
		default:
			_ = process.cmd.Process.Kill()
			select {
			case <-process.done:
			case <-time.After(2 * time.Second):
			}
		}
	})
	return process
}

func environmentWith(key, value string) []string {
	prefix := key + "="
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			environment = append(environment, entry)
		}
	}
	return append(environment, prefix+value)
}

func (p *workerProcess) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL worker: %v\n%s", err, p.output.String())
	}
	select {
	case <-p.done:
		err := p.waitError()
		if err == nil {
			t.Fatalf("SIGKILL worker exited successfully\n%s", p.output.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("SIGKILL worker did not exit\n%s", p.output.String())
	}
}

func (p *workerProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM worker: %v\n%s", err, p.output.String())
	}
	select {
	case <-p.done:
		err := p.waitError()
		if err != nil {
			t.Fatalf("worker shutdown: %v\n%s", err, p.output.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("worker did not shut down\n%s", p.output.String())
	}
}

func (p *workerProcess) waitError() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func waitForProcessClaim(t *testing.T, store *postgres.Store, conn *pgx.Conn, id, owner string) postgres.Claim {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var version int64
		var expiresAt time.Time
		err := conn.QueryRow(context.Background(), `SELECT lease_version, lease_expires_at
			FROM jobs WHERE id = $1 AND state = 'running' AND lease_owner = $2`, id, owner).
			Scan(&version, &expiresAt)
		if err == nil {
			job, err := store.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			return postgres.Claim{Job: job, Owner: owner, Version: version, ExpiresAt: expiresAt}
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker %s did not claim job %s", owner, id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitForTerminalJob(t *testing.T, store *postgres.Store, id string, timeout time.Duration) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		job, err := store.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State.Terminal() {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish; last state: %s attempts=%d", id, job.State, job.Attempts)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
