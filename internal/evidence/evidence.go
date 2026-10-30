// Package evidence validates a collected measurement bundle before it is used
// to write a report. It verifies provenance hashes and the workload's declared
// completion gates; it does not calculate performance claims.
package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/loadgen"
)

const schemaVersion = 1

// Summary describes the validated workload without deriving latency or
// throughput statistics from a shared runner.
type Summary struct {
	SchemaVersion       int    `json:"schema_version"`
	CommitSHA           string `json:"commit_sha"`
	ScenarioCount       int    `json:"scenario_count"`
	RequestsPerScenario int    `json:"requests_per_scenario"`
	ExpectedJobs        int    `json:"expected_jobs"`
	WorkerConcurrency   int    `json:"worker_concurrency"`
	Concurrencies       []int  `json:"admission_concurrencies"`
}

// Policy binds a bundle to the source revision expected by its consumer.
// RequireCleanTree prevents measurements from uncommitted tracked changes from
// being presented as evidence for that revision.
type Policy struct {
	ExpectedCommitSHA string
	RequireCleanTree  bool
}

type contextFile struct {
	SchemaVersion int    `json:"schema_version"`
	CapturedAt    string `json:"captured_at"`
	Source        struct {
		CommitSHA        string `json:"commit_sha"`
		TrackedTreeDirty bool   `json:"tracked_tree_dirty"`
	} `json:"source"`
	Stack struct {
		Database          string `json:"database"`
		WorkerProcesses   int    `json:"worker_processes"`
		WorkerConcurrency int    `json:"worker_concurrency"`
		VolumePolicy      string `json:"volume_policy"`
	} `json:"stack"`
	Matrix struct {
		RequestsPerRun    int    `json:"requests_per_run"`
		Repetitions       int    `json:"repetitions"`
		Concurrency       []int  `json:"concurrency"`
		DelayMS           int    `json:"delay_ms"`
		FailFirstAttempts int    `json:"fail_first_attempts"`
		MaxAttempts       int    `json:"max_attempts"`
		TimeoutMS         int    `json:"timeout_ms"`
		RequestTimeout    string `json:"request_timeout"`
	} `json:"matrix"`
	Runner struct {
		GitHubActions       bool   `json:"github_actions"`
		RunID               string `json:"run_id"`
		RunAttempt          string `json:"run_attempt"`
		RunnerName          string `json:"runner_name"`
		RunnerOS            string `json:"runner_os"`
		RunnerArch          string `json:"runner_arch"`
		SharedOrVirtualized bool   `json:"shared_or_virtualized"`
	} `json:"runner"`
	Host struct {
		Platform       string `json:"platform"`
		Machine        string `json:"machine"`
		CPUCount       int    `json:"cpu_count"`
		CPUModel       string `json:"cpu_model"`
		MemoryTotalKiB int64  `json:"memory_total_kib"`
	} `json:"host"`
	Toolchain struct {
		Go            string `json:"go"`
		DockerServer  string `json:"docker_server"`
		DockerCompose string `json:"docker_compose"`
	} `json:"toolchain"`
	Limitations []string `json:"limitations"`
}

type manifestFile struct {
	SchemaVersion int `json:"schema_version"`
	Files         []struct {
		File   string `json:"file"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

type scenarioIndex struct {
	SchemaVersion int `json:"schema_version"`
	Scenarios     []struct {
		File        string `json:"file"`
		Concurrency int    `json:"concurrency"`
		Repetition  int    `json:"repetition"`
		SHA256      string `json:"sha256"`
	} `json:"scenarios"`
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// Validate verifies a bundle without applying caller-specific provenance
// policy. Report tooling should use ValidateWithPolicy.
func Validate(dir string) (Summary, error) {
	return ValidateWithPolicy(dir, Policy{})
}

// ValidateWithPolicy verifies all JSON files covered by the manifest, the
// scenario matrix, Prometheus completion snapshots and caller provenance.
func ValidateWithPolicy(dir string, policy Policy) (Summary, error) {
	var manifest manifestFile
	if err := readJSON(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return Summary{}, err
	}
	if manifest.SchemaVersion != schemaVersion {
		return Summary{}, fmt.Errorf("manifest schema version %d", manifest.SchemaVersion)
	}
	manifestHashes, err := validateManifest(dir, manifest)
	if err != nil {
		return Summary{}, err
	}

	var context contextFile
	if err := readCoveredJSON(dir, "context.json", manifestHashes, &context); err != nil {
		return Summary{}, err
	}
	if context.SchemaVersion != schemaVersion || context.Source.CommitSHA == "" {
		return Summary{}, errors.New("context has invalid schema or empty commit SHA")
	}
	if policy.ExpectedCommitSHA != "" && context.Source.CommitSHA != policy.ExpectedCommitSHA {
		return Summary{}, fmt.Errorf("context commit %q, expected %q", context.Source.CommitSHA, policy.ExpectedCommitSHA)
	}
	if policy.RequireCleanTree && context.Source.TrackedTreeDirty {
		return Summary{}, errors.New("context records a dirty tracked tree")
	}
	if context.Matrix.RequestsPerRun < 1 || context.Matrix.Repetitions < 1 || len(context.Matrix.Concurrency) == 0 {
		return Summary{}, errors.New("context has an empty workload matrix")
	}
	seenConcurrency := make(map[int]struct{}, len(context.Matrix.Concurrency))
	for _, concurrency := range context.Matrix.Concurrency {
		if concurrency < 1 {
			return Summary{}, errors.New("context has invalid admission concurrency")
		}
		if _, exists := seenConcurrency[concurrency]; exists {
			return Summary{}, errors.New("context has duplicate admission concurrency")
		}
		seenConcurrency[concurrency] = struct{}{}
	}
	if context.Stack.WorkerConcurrency < 1 {
		return Summary{}, errors.New("context has invalid worker concurrency")
	}
	expectedJobs := context.Matrix.RequestsPerRun * context.Matrix.Repetitions * len(context.Matrix.Concurrency)

	var index scenarioIndex
	if err := readCoveredJSON(dir, "scenario-index.json", manifestHashes, &index); err != nil {
		return Summary{}, err
	}
	if err := validateScenarios(dir, context, index, manifestHashes); err != nil {
		return Summary{}, err
	}
	if err := validatePrometheus(dir, expectedJobs, manifestHashes); err != nil {
		return Summary{}, err
	}
	if err := validateJSONArray(dir, "compose-ps.json", manifestHashes); err != nil {
		return Summary{}, err
	}
	if err := validateJSONArray(dir, "docker-stats-final.json", manifestHashes); err != nil {
		return Summary{}, err
	}

	return Summary{
		SchemaVersion: schemaVersion, CommitSHA: context.Source.CommitSHA,
		ScenarioCount: len(index.Scenarios), RequestsPerScenario: context.Matrix.RequestsPerRun,
		ExpectedJobs: expectedJobs, WorkerConcurrency: context.Stack.WorkerConcurrency,
		Concurrencies: append([]int(nil), context.Matrix.Concurrency...),
	}, nil
}

func validateManifest(dir string, manifest manifestFile) (map[string]string, error) {
	hashes := make(map[string]string, len(manifest.Files))
	for _, entry := range manifest.Files {
		if entry.File == "" || entry.File != filepath.Base(entry.File) || entry.File == "manifest.json" || !strings.HasSuffix(entry.File, ".json") {
			return nil, fmt.Errorf("unsafe manifest file %q", entry.File)
		}
		if _, exists := hashes[entry.File]; exists {
			return nil, fmt.Errorf("duplicate manifest file %q", entry.File)
		}
		if len(entry.SHA256) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid SHA-256 for %q", entry.File)
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.File))
		if err != nil {
			return nil, fmt.Errorf("read manifest file %q: %w", entry.File, err)
		}
		digest := sha256.Sum256(data)
		actual := hex.EncodeToString(digest[:])
		if int64(len(data)) != entry.Bytes || actual != entry.SHA256 {
			return nil, fmt.Errorf("manifest mismatch for %q", entry.File)
		}
		hashes[entry.File] = actual
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		name := filepath.Base(path)
		if name != "manifest.json" {
			if _, ok := hashes[name]; !ok {
				return nil, fmt.Errorf("JSON file %q is not covered by manifest", name)
			}
		}
	}
	return hashes, nil
}

func validateScenarios(dir string, context contextFile, index scenarioIndex, manifest map[string]string) error {
	if index.SchemaVersion != schemaVersion {
		return fmt.Errorf("scenario index schema version %d", index.SchemaVersion)
	}
	expected := make(map[string]struct{})
	for _, concurrency := range context.Matrix.Concurrency {
		for repetition := 1; repetition <= context.Matrix.Repetitions; repetition++ {
			expected[fmt.Sprintf("scenario-c%d-r%d.json", concurrency, repetition)] = struct{}{}
		}
	}
	if len(index.Scenarios) != len(expected) {
		return fmt.Errorf("scenario count %d, want %d", len(index.Scenarios), len(expected))
	}
	seen := make(map[string]struct{}, len(index.Scenarios))
	requestTimeout, err := time.ParseDuration(context.Matrix.RequestTimeout)
	if err != nil || requestTimeout <= 0 {
		return errors.New("context has invalid request timeout")
	}
	var baselineTarget, baselinePayloadSHA string
	var baselinePayloadBytes int
	for _, scenario := range index.Scenarios {
		if _, ok := expected[scenario.File]; !ok {
			return fmt.Errorf("unexpected scenario %q", scenario.File)
		}
		if scenario.File != fmt.Sprintf("scenario-c%d-r%d.json", scenario.Concurrency, scenario.Repetition) {
			return fmt.Errorf("scenario %q does not match its index dimensions", scenario.File)
		}
		if _, ok := seen[scenario.File]; ok {
			return fmt.Errorf("duplicate scenario %q", scenario.File)
		}
		seen[scenario.File] = struct{}{}
		if manifest[scenario.File] != scenario.SHA256 {
			return fmt.Errorf("scenario index hash mismatch for %q", scenario.File)
		}
		var report loadgen.Summary
		if err := readCoveredJSON(dir, scenario.File, manifest, &report); err != nil {
			return err
		}
		if report.SchemaVersion != schemaVersion || report.Workload.Concurrency != scenario.Concurrency || report.Workload.Requests != context.Matrix.RequestsPerRun {
			return fmt.Errorf("scenario %q workload does not match index/context", scenario.File)
		}
		if report.Workload.RequestTimeoutMS != requestTimeout.Milliseconds() ||
			report.Workload.DelayMS != context.Matrix.DelayMS ||
			report.Workload.FailFirstAttempts != context.Matrix.FailFirstAttempts ||
			report.Workload.MaxAttempts != context.Matrix.MaxAttempts ||
			report.Workload.TimeoutMS != context.Matrix.TimeoutMS {
			return fmt.Errorf("scenario %q task controls do not match context", scenario.File)
		}
		if baselineTarget == "" {
			if report.Target == "" || len(report.Workload.PayloadSHA256) != sha256.Size*2 {
				return fmt.Errorf("scenario %q has incomplete target or payload identity", scenario.File)
			}
			baselineTarget = report.Target
			baselinePayloadSHA = report.Workload.PayloadSHA256
			baselinePayloadBytes = report.Workload.PayloadBytes
		} else if report.Target != baselineTarget || report.Workload.PayloadSHA256 != baselinePayloadSHA || report.Workload.PayloadBytes != baselinePayloadBytes {
			return fmt.Errorf("scenario %q target or payload differs from the matrix", scenario.File)
		}
		counts := report.Counts
		outcomes := counts.Accepted + counts.Replayed + counts.QueueFull + counts.RecordCapacity + counts.Draining + counts.OtherClientError + counts.ServerError + counts.OtherStatus + counts.TransportError
		if counts.Offered != context.Matrix.RequestsPerRun || outcomes != counts.Offered || counts.Accepted != counts.Offered {
			return fmt.Errorf("scenario %q has incomplete or rejected admissions", scenario.File)
		}
		if len(report.Latency.Buckets) == 0 || report.Latency.Buckets[len(report.Latency.Buckets)-1].LEMS != "+Inf" || report.Latency.Buckets[len(report.Latency.Buckets)-1].Count != counts.Offered {
			return fmt.Errorf("scenario %q has invalid +Inf bucket", scenario.File)
		}
	}
	return nil
}

func validatePrometheus(dir string, expectedJobs int, manifest map[string]string) error {
	targets, err := readPrometheus(dir, "prometheus-targets.json", manifest)
	if err != nil {
		return err
	}
	if len(targets.Data.Result) != 2 {
		return fmt.Errorf("Prometheus target count %d, want 2", len(targets.Data.Result))
	}
	wantTargets := map[string]bool{"small-chain-api": false, "small-chain-worker": false}
	for _, result := range targets.Data.Result {
		value, err := resultValue(result.Value)
		if err != nil || value != 1 {
			return errors.New("Prometheus target is not up")
		}
		job := result.Metric["job"]
		if _, ok := wantTargets[job]; !ok || wantTargets[job] {
			return fmt.Errorf("unexpected or duplicate Prometheus target %q", job)
		}
		wantTargets[job] = true
	}
	health, err := singlePrometheusValue(dir, "prometheus-state-health.json", manifest)
	if err != nil || health != 1 {
		return errors.New("durable state metric collection is not healthy")
	}
	states, err := readPrometheus(dir, "prometheus-states.json", manifest)
	if err != nil {
		return err
	}
	stateValues := make(map[string]float64)
	for _, result := range states.Data.Result {
		value, err := resultValue(result.Value)
		if err != nil {
			return err
		}
		stateValues[result.Metric["state"]] = value
	}
	if stateValues["succeeded"] != float64(expectedJobs) || stateValues["queued"]+stateValues["running"]+stateValues["retrying"] != 0 {
		return fmt.Errorf("durable states did not converge to %d successes", expectedJobs)
	}
	workerCount, err := singlePrometheusValue(dir, "prometheus-worker-count.json", manifest)
	if err != nil || workerCount < float64(expectedJobs) {
		return fmt.Errorf("worker attempt count %.0f, want at least %d", workerCount, expectedJobs)
	}
	workerSum, err := singlePrometheusValue(dir, "prometheus-worker-sum.json", manifest)
	if err != nil || workerSum < 0 {
		return errors.New("worker duration sum is invalid")
	}
	buckets, err := readPrometheus(dir, "prometheus-worker-buckets.json", manifest)
	if err != nil {
		return err
	}
	inf := -1.0
	for _, result := range buckets.Data.Result {
		if result.Metric["le"] == "+Inf" {
			inf, err = resultValue(result.Value)
			if err != nil {
				return err
			}
		}
	}
	if inf < float64(expectedJobs) {
		return fmt.Errorf("worker +Inf bucket %.0f, want at least %d", inf, expectedJobs)
	}
	return nil
}

func readPrometheus(dir, name string, manifest map[string]string) (prometheusResponse, error) {
	var response prometheusResponse
	if err := readCoveredJSON(dir, name, manifest, &response); err != nil {
		return response, err
	}
	if response.Status != "success" || response.Data.ResultType != "vector" {
		return response, fmt.Errorf("%s is not a successful Prometheus vector", name)
	}
	return response, nil
}

func singlePrometheusValue(dir, name string, manifest map[string]string) (float64, error) {
	response, err := readPrometheus(dir, name, manifest)
	if err != nil {
		return 0, err
	}
	if len(response.Data.Result) != 1 {
		return 0, fmt.Errorf("%s has %d results, want 1", name, len(response.Data.Result))
	}
	return resultValue(response.Data.Result[0].Value)
}

func resultValue(value []json.RawMessage) (float64, error) {
	if len(value) != 2 {
		return 0, errors.New("Prometheus sample has invalid value tuple")
	}
	var text string
	if err := json.Unmarshal(value[1], &text); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(text, 64)
}

func validateJSONArray(dir, name string, manifest map[string]string) error {
	var values []json.RawMessage
	if err := readCoveredJSON(dir, name, manifest, &values); err != nil {
		return err
	}
	if len(values) == 0 {
		return fmt.Errorf("%s is empty", name)
	}
	return nil
}

func readCoveredJSON(dir, name string, manifest map[string]string, target any) error {
	expected, ok := manifest[name]
	if !ok {
		return fmt.Errorf("required file %q is not covered by manifest", name)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != expected {
		return fmt.Errorf("manifest mismatch for %q", name)
	}
	return decodeJSON(name, data, target)
}

func readJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode %s: trailing data", filepath.Base(path))
	}
	return nil
}

func decodeJSON(name string, data []byte, target any) error {
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(data), 8<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("decode %s: trailing data", name)
	}
	return nil
}
