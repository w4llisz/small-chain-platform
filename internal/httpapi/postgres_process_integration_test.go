//go:build integration

package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func TestPostgresHTTPPersistsAdmissionAcrossRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process lifecycle contract uses Linux SIGTERM semantics")
	}
	binary := os.Getenv("SMALL_CHAIN_API_BINARY")
	if binary == "" {
		t.Fatal("SMALL_CHAIN_API_BINARY is required for process integration tests")
	}
	if info, err := os.Stat(binary); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("API binary is not executable: %s: %v", binary, err)
	}
	dsn := postgresHTTPDatabase(t)

	first := startAPIProcess(t, binary, dsn)
	base := "http://" + first.address(t)
	if status, body, _ := httpRequest(t, http.MethodGet, base+"/readyz", "", ""); status != http.StatusOK {
		t.Fatalf("ready: %d %s", status, body)
	}
	spec := `{"kind":"demo.checksum","payload":"durable restart"}`
	status, body, header := httpRequest(t, http.MethodPost, base+"/v1/jobs", "durable-http", spec)
	if status != http.StatusAccepted || header.Get("Idempotency-Replayed") != "false" {
		t.Fatalf("submit: %d %s", status, body)
	}
	var admitted jobs.Job
	if err := json.Unmarshal(body, &admitted); err != nil || admitted.ID == "" || admitted.State != jobs.Queued {
		t.Fatalf("admitted: %+v err=%v body=%s", admitted, err, body)
	}
	if status, body, _ := httpRequest(t, http.MethodPost, base+"/v1/jobs", "over-capacity", spec); status != http.StatusServiceUnavailable || !bytes.Contains(body, []byte(`"code":"record_capacity"`)) {
		t.Fatalf("capacity: %d %s", status, body)
	}
	first.stop(t)

	second := startAPIProcess(t, binary, dsn)
	base = "http://" + second.address(t)
	status, body, header = httpRequest(t, http.MethodPost, base+"/v1/jobs", "durable-http", spec)
	var replay jobs.Job
	if err := json.Unmarshal(body, &replay); err != nil || status != http.StatusOK || header.Get("Idempotency-Replayed") != "true" || replay.ID != admitted.ID {
		t.Fatalf("restart replay: status=%d replay=%+v err=%v body=%s", status, replay, err, body)
	}
	status, body, _ = httpRequest(t, http.MethodGet, base+"/v1/jobs/"+admitted.ID, "", "")
	var persisted jobs.Job
	if err := json.Unmarshal(body, &persisted); err != nil || status != http.StatusOK || persisted.ID != admitted.ID || persisted.State != jobs.Queued {
		t.Fatalf("restart get: status=%d job=%+v err=%v body=%s", status, persisted, err, body)
	}
	status, body, _ = httpRequest(t, http.MethodPost, base+"/v1/jobs", "durable-http", `{"kind":"demo.checksum","payload":"changed"}`)
	if status != http.StatusConflict {
		t.Fatalf("restart conflict: %d %s", status, body)
	}
	status, body, _ = httpRequest(t, http.MethodPost, base+"/v1/jobs/"+admitted.ID+"/cancel", "", "")
	var canceled jobs.Job
	if err := json.Unmarshal(body, &canceled); err != nil || status != http.StatusOK || canceled.ID != admitted.ID || canceled.State != jobs.Canceled {
		t.Fatalf("restart cancel: status=%d job=%+v err=%v body=%s", status, canceled, err, body)
	}
	status, body, _ = httpRequest(t, http.MethodGet, base+"/v1/jobs/"+admitted.ID, "", "")
	if err := json.Unmarshal(body, &persisted); err != nil || status != http.StatusOK || persisted.State != jobs.Canceled {
		t.Fatalf("persisted cancel: status=%d job=%+v err=%v body=%s", status, persisted, err, body)
	}
	ageJobForRetention(t, dsn, admitted.ID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body, header = httpRequest(t, http.MethodPost, base+"/v1/jobs", "after-retention", spec)
		if status == http.StatusAccepted && header.Get("Idempotency-Replayed") == "false" {
			break
		}
		if status != http.StatusServiceUnavailable || !bytes.Contains(body, []byte(`"code":"record_capacity"`)) {
			t.Fatalf("submit while waiting for retention: %d %s", status, body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled retention did not release capacity: %s", second.output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, body, _ = httpRequest(t, http.MethodGet, base+"/metrics", "", "")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`small_chain_backend_info{storage="postgres"} 1`)) {
		t.Fatalf("postgres metrics: %d %s", status, body)
	}
	second.stop(t)
}

func postgresHTTPDatabase(t *testing.T) string {
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
	schema := fmt.Sprintf("sc_http_test_%d", time.Now().UnixNano())
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
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.ConfigureAdmission(ctx, 1, time.Hour); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	return u.String()
}

type apiProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	output  synchronizedBuffer
	waitMu  sync.Mutex
	waitErr error
}

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestSynchronizedProcessLogCapture(t *testing.T) {
	var output synchronizedBuffer
	reader, writer := io.Pipe()
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(&output, reader)
		copied <- err
	}()
	produced := make(chan error, 1)
	go func() {
		for range 1000 {
			if _, err := writer.Write([]byte("process log\n")); err != nil {
				produced <- err
				return
			}
		}
		produced <- writer.Close()
	}()
	for {
		_ = output.String()
		select {
		case err := <-copied:
			if err != nil {
				t.Fatal(err)
			}
			if err := <-produced; err != nil {
				t.Fatal(err)
			}
			return
		default:
			runtime.Gosched()
		}
	}
}

func startAPIProcess(t *testing.T, binary, dsn string) *apiProcess {
	t.Helper()
	process := &apiProcess{done: make(chan struct{})}
	process.cmd = exec.Command(binary, "-storage=postgres", "-addr=127.0.0.1:0", "-shutdown-grace=2s", "-retention-interval=50ms", "-retention-batch=1")
	process.cmd.Env = environmentWithDatabaseURL(dsn)
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

func ageJobForRetention(t *testing.T, dsn, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	command, err := connection.Exec(ctx, `UPDATE jobs
SET updated_at = statement_timestamp() - interval '2 hours'
WHERE id = $1 AND state = 'canceled'`, id)
	if err != nil {
		t.Fatal(err)
	}
	if command.RowsAffected() != 1 {
		t.Fatalf("aged rows = %d, want 1", command.RowsAffected())
	}
}

func (p *apiProcess) address(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, line := range strings.Split(p.output.String(), "\n") {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) == nil && event["msg"] == "service.started" {
				address, ok := event["address"].(string)
				if !ok || address == "" || event["storage"] != "postgres" {
					t.Fatalf("invalid start event: %v", event)
				}
				return address
			}
		}
		select {
		case <-p.done:
			t.Fatalf("API exited before startup: %v\n%s", p.waitError(), p.output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("API did not start\n%s", p.output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p *apiProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM API: %v\n%s", err, p.output.String())
	}
	select {
	case <-p.done:
		if err := p.waitError(); err != nil {
			t.Fatalf("API shutdown: %v\n%s", err, p.output.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("API did not shut down\n%s", p.output.String())
	}
}

func (p *apiProcess) waitError() error {
	p.waitMu.Lock()
	defer p.waitMu.Unlock()
	return p.waitErr
}

func environmentWithDatabaseURL(dsn string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "DATABASE_URL=") {
			environment = append(environment, entry)
		}
	}
	return append(environment, "DATABASE_URL="+dsn)
}

func httpRequest(t *testing.T, method, target, key, body string) (int, []byte, http.Header) {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, encoded, response.Header.Clone()
}
