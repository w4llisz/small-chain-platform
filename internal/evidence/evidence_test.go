package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w4llisz/small-chain-platform/internal/loadgen"
)

func TestValidate(t *testing.T) {
	dir := writeFixture(t)
	summary, err := Validate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CommitSHA != strings.Repeat("a", 40) || summary.ScenarioCount != 4 || summary.ExpectedJobs != 12 || summary.WorkerConcurrency != 4 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestValidateRejectsTampering(t *testing.T) {
	dir := writeFixture(t)
	path := filepath.Join(dir, "scenario-c1-r1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(dir); err == nil || !strings.Contains(err.Error(), "manifest mismatch") {
		t.Fatalf("error = %v, want manifest mismatch", err)
	}
}

func TestValidateRejectsUncoveredJSON(t *testing.T) {
	dir := writeFixture(t)
	writeTestJSON(t, filepath.Join(dir, "unreviewed.json"), map[string]any{"selected": true})
	if _, err := Validate(dir); err == nil || !strings.Contains(err.Error(), "not covered by manifest") {
		t.Fatalf("error = %v, want uncovered JSON failure", err)
	}
}

func TestValidateRejectsWrongExpectedCommit(t *testing.T) {
	dir := writeFixture(t)
	_, err := ValidateWithPolicy(dir, Policy{ExpectedCommitSHA: strings.Repeat("b", 40)})
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("error = %v, want commit mismatch", err)
	}
}

func TestValidateRejectsDirtyTreeWhenRequired(t *testing.T) {
	dir := writeFixture(t)
	path := filepath.Join(dir, "context.json")
	var context map[string]any
	readTestJSON(t, path, &context)
	context["source"].(map[string]any)["tracked_tree_dirty"] = true
	writeTestJSON(t, path, context)
	writeManifest(t, dir)
	_, err := ValidateWithPolicy(dir, Policy{
		ExpectedCommitSHA: strings.Repeat("a", 40),
		RequireCleanTree:  true,
	})
	if err == nil || !strings.Contains(err.Error(), "dirty tracked tree") {
		t.Fatalf("error = %v, want dirty-tree rejection", err)
	}
}

func TestValidateRejectsRejectedAdmissionEvenWithValidHashes(t *testing.T) {
	dir := writeFixture(t)
	path := filepath.Join(dir, "scenario-c1-r1.json")
	var report loadgen.Summary
	readTestJSON(t, path, &report)
	report.Counts.Accepted--
	report.Counts.QueueFull++
	writeTestJSON(t, path, report)
	rewriteScenarioHash(t, dir, "scenario-c1-r1.json")
	writeManifest(t, dir)
	if _, err := Validate(dir); err == nil || !strings.Contains(err.Error(), "rejected admissions") {
		t.Fatalf("error = %v, want rejected admissions", err)
	}
}

func TestValidateRejectsIncompleteConvergence(t *testing.T) {
	dir := writeFixture(t)
	path := filepath.Join(dir, "prometheus-states.json")
	writeTestJSON(t, path, promVector(
		promSample(map[string]string{"state": "succeeded"}, "11"),
		promSample(map[string]string{"state": "running"}, "1"),
	))
	writeManifest(t, dir)
	if _, err := Validate(dir); err == nil || !strings.Contains(err.Error(), "did not converge") {
		t.Fatalf("error = %v, want convergence failure", err)
	}
}

func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTestJSON(t, filepath.Join(dir, "context.json"), map[string]any{
		"schema_version": 1,
		"source": map[string]any{
			"commit_sha": strings.Repeat("a", 40), "tracked_tree_dirty": false,
		},
		"stack":  map[string]any{"worker_concurrency": 4},
		"matrix": map[string]any{"requests_per_run": 3, "repetitions": 2, "concurrency": []int{1, 4}},
	})

	entries := make([]map[string]any, 0, 4)
	for _, concurrency := range []int{1, 4} {
		for repetition := 1; repetition <= 2; repetition++ {
			name := fmt.Sprintf("scenario-c%d-r%d.json", concurrency, repetition)
			report := loadgen.Summary{SchemaVersion: 1}
			report.Workload.Requests = 3
			report.Workload.Concurrency = concurrency
			report.Counts.Offered = 3
			report.Counts.Accepted = 3
			report.Latency.Buckets = []loadgen.Bucket{{LEMS: "1", Count: 2}, {LEMS: "+Inf", Count: 3}}
			writeTestJSON(t, filepath.Join(dir, name), report)
			entries = append(entries, map[string]any{
				"file": name, "concurrency": concurrency, "repetition": repetition,
				"sha256": fileHash(t, filepath.Join(dir, name)),
			})
		}
	}
	writeTestJSON(t, filepath.Join(dir, "scenario-index.json"), map[string]any{"schema_version": 1, "scenarios": entries})
	writeTestJSON(t, filepath.Join(dir, "prometheus-targets.json"), promVector(
		promSample(map[string]string{"job": "small-chain-api"}, "1"),
		promSample(map[string]string{"job": "small-chain-worker"}, "1"),
	))
	writeTestJSON(t, filepath.Join(dir, "prometheus-state-health.json"), promVector(promSample(nil, "1")))
	writeTestJSON(t, filepath.Join(dir, "prometheus-states.json"), promVector(
		promSample(map[string]string{"state": "succeeded"}, "12"),
		promSample(map[string]string{"state": "queued"}, "0"),
		promSample(map[string]string{"state": "running"}, "0"),
		promSample(map[string]string{"state": "retrying"}, "0"),
	))
	writeTestJSON(t, filepath.Join(dir, "prometheus-worker-count.json"), promVector(promSample(nil, "12")))
	writeTestJSON(t, filepath.Join(dir, "prometheus-worker-sum.json"), promVector(promSample(nil, "0.3")))
	writeTestJSON(t, filepath.Join(dir, "prometheus-worker-buckets.json"), promVector(
		promSample(map[string]string{"le": "0.1"}, "8"),
		promSample(map[string]string{"le": "+Inf"}, "12"),
	))
	writeTestJSON(t, filepath.Join(dir, "compose-ps.json"), []map[string]any{{"Service": "api", "State": "running"}})
	writeTestJSON(t, filepath.Join(dir, "docker-stats-final.json"), []map[string]any{{"Name": "api", "MemUsage": "1MiB"}})
	writeManifest(t, dir)
	return dir
}

func rewriteScenarioHash(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, "scenario-index.json")
	var index map[string]any
	readTestJSON(t, path, &index)
	for _, raw := range index["scenarios"].([]any) {
		entry := raw.(map[string]any)
		if entry["file"] == name {
			entry["sha256"] = fileHash(t, filepath.Join(dir, name))
		}
	}
	writeTestJSON(t, path, index)
}

func writeManifest(t *testing.T, dir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		if filepath.Base(path) == "manifest.json" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, map[string]any{
			"file": filepath.Base(path), "bytes": info.Size(), "sha256": fileHash(t, path),
		})
	}
	writeTestJSON(t, filepath.Join(dir, "manifest.json"), map[string]any{"schema_version": 1, "files": entries})
}

func promVector(samples ...map[string]any) map[string]any {
	return map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": samples}}
}

func promSample(metric map[string]string, value string) map[string]any {
	if metric == nil {
		metric = map[string]string{}
	}
	return map[string]any{"metric": metric, "value": []any{1_700_000_000, value}}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
