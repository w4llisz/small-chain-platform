package evidence

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/w4llisz/small-chain-platform/internal/loadgen"
)

type scenarioObservation struct {
	Concurrency int
	Repetition  int
	Report      loadgen.Summary
}

// RenderMarkdown validates a bundle and renders a deterministic, descriptive
// report. The report deliberately avoids percentiles, throughput and SLOs.
func RenderMarkdown(dir string, policy Policy) (string, error) {
	summary, err := ValidateWithPolicy(dir, policy)
	if err != nil {
		return "", err
	}

	var manifest manifestFile
	if err := readJSON(filepath.Join(dir, "manifest.json"), &manifest); err != nil {
		return "", err
	}
	hashes, err := validateManifest(dir, manifest)
	if err != nil {
		return "", err
	}
	var context contextFile
	if err := readCoveredJSON(dir, "context.json", hashes, &context); err != nil {
		return "", err
	}
	var index scenarioIndex
	if err := readCoveredJSON(dir, "scenario-index.json", hashes, &index); err != nil {
		return "", err
	}

	observations := make([]scenarioObservation, 0, len(index.Scenarios))
	for _, scenario := range index.Scenarios {
		var report loadgen.Summary
		if err := readCoveredJSON(dir, scenario.File, hashes, &report); err != nil {
			return "", err
		}
		observations = append(observations, scenarioObservation{
			Concurrency: scenario.Concurrency,
			Repetition:  scenario.Repetition,
			Report:      report,
		})
	}
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].Concurrency != observations[j].Concurrency {
			return observations[i].Concurrency < observations[j].Concurrency
		}
		return observations[i].Repetition < observations[j].Repetition
	})
	workerCount, err := singlePrometheusValue(dir, "prometheus-worker-count.json", hashes)
	if err != nil {
		return "", err
	}
	workerSum, err := singlePrometheusValue(dir, "prometheus-worker-sum.json", hashes)
	if err != nil {
		return "", err
	}

	var out bytes.Buffer
	fmt.Fprintln(&out, "# Small Chain measurement evidence")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "> Deterministic rendering of a validated evidence bundle. These observations describe this runner and workload; they are not a production benchmark, percentile, throughput claim, alert threshold, or SLO.")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "## Provenance")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| Field | Observed value |")
	fmt.Fprintln(&out, "| --- | --- |")
	writeRow(&out, "Commit", summary.CommitSHA)
	writeRow(&out, "Captured at", context.CapturedAt)
	treeState := "clean"
	if context.Source.TrackedTreeDirty {
		treeState = "dirty"
	}
	writeRow(&out, "Tracked tree", treeState)
	writeRow(&out, "Runner", runnerDescription(context))
	writeRow(&out, "Host", hostDescription(context))
	writeRow(&out, "Go", context.Toolchain.Go)
	writeRow(&out, "Docker server", context.Toolchain.DockerServer)
	writeRow(&out, "Docker Compose", context.Toolchain.DockerCompose)

	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "## Workload")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| Field | Configured value |")
	fmt.Fprintln(&out, "| --- | --- |")
	writeRow(&out, "Database", context.Stack.Database)
	writeRow(&out, "Worker processes", strconv.Itoa(context.Stack.WorkerProcesses))
	writeRow(&out, "Worker concurrency", strconv.Itoa(summary.WorkerConcurrency))
	writeRow(&out, "Volume policy", context.Stack.VolumePolicy)
	writeRow(&out, "Admission concurrency", intList(summary.Concurrencies))
	writeRow(&out, "Repetitions per concurrency", strconv.Itoa(context.Matrix.Repetitions))
	writeRow(&out, "Requests per repetition", strconv.Itoa(summary.RequestsPerScenario))
	if len(observations) > 0 {
		writeRow(&out, "Admission target", observations[0].Report.Target)
		writeRow(&out, "Payload bytes", strconv.Itoa(observations[0].Report.Workload.PayloadBytes))
		writeRow(&out, "Payload SHA-256", observations[0].Report.Workload.PayloadSHA256)
	}
	writeRow(&out, "Task delay", fmt.Sprintf("%d ms", context.Matrix.DelayMS))
	writeRow(&out, "Injected failures before success", strconv.Itoa(context.Matrix.FailFirstAttempts))
	writeRow(&out, "Task attempt budget", strconv.Itoa(context.Matrix.MaxAttempts))
	writeRow(&out, "Task timeout", fmt.Sprintf("%d ms", context.Matrix.TimeoutMS))
	writeRow(&out, "Request timeout", context.Matrix.RequestTimeout)

	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "## Admission observations")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "All offered requests were accepted. Millisecond values are copied from the validated generator output and rounded to three decimal places; no percentile or rate is derived.")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| Concurrency | Repetition | Offered | Accepted | Client run duration (ms) | Admission min (ms) | Admission mean (ms) | Admission max (ms) |")
	fmt.Fprintln(&out, "| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, observation := range observations {
		report := observation.Report
		fmt.Fprintf(&out, "| %d | %d | %d | %d | %s | %s | %s | %s |\n",
			observation.Concurrency,
			observation.Repetition,
			report.Counts.Offered,
			report.Counts.Accepted,
			decimal(report.DurationMS),
			decimal(report.Latency.MinMS),
			decimal(report.Latency.MeanMS),
			decimal(report.Latency.MaxMS),
		)
	}

	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "## Completion evidence")
	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "| Check | Observed value |")
	fmt.Fprintln(&out, "| --- | ---: |")
	writeRow(&out, "Durable succeeded jobs", strconv.Itoa(summary.ExpectedJobs))
	writeRow(&out, "Active queued/running/retrying jobs", "0")
	writeRow(&out, "Worker attempt count snapshot", number(workerCount))
	writeRow(&out, "Worker executor-duration sum snapshot (seconds)", number(workerSum))

	fmt.Fprintln(&out)
	fmt.Fprintln(&out, "## Limitations")
	fmt.Fprintln(&out)
	for _, limitation := range context.Limitations {
		fmt.Fprintf(&out, "- %s\n", markdownText(limitation))
	}
	return out.String(), nil
}

func writeRow(out *bytes.Buffer, field, value string) {
	if value == "" {
		value = "not recorded"
	}
	fmt.Fprintf(out, "| %s | %s |\n", markdownText(field), markdownText(value))
}

func runnerDescription(context contextFile) string {
	kind := "local or unknown"
	if context.Runner.GitHubActions {
		kind = "GitHub Actions shared/virtualized runner"
	} else if context.Runner.SharedOrVirtualized {
		kind = "shared/virtualized runner"
	}
	parts := []string{kind, context.Runner.RunnerOS, context.Runner.RunnerArch}
	return joinRecorded(parts)
}

func hostDescription(context contextFile) string {
	parts := []string{context.Host.Platform, context.Host.Machine, context.Host.CPUModel}
	if context.Host.CPUCount > 0 {
		parts = append(parts, fmt.Sprintf("%d CPUs", context.Host.CPUCount))
	}
	if context.Host.MemoryTotalKiB > 0 {
		parts = append(parts, fmt.Sprintf("%d KiB memory", context.Host.MemoryTotalKiB))
	}
	return joinRecorded(parts)
}

func joinRecorded(values []string) string {
	nonEmpty := values[:0]
	for _, value := range values {
		if value != "" {
			nonEmpty = append(nonEmpty, value)
		}
	}
	return strings.Join(nonEmpty, "; ")
}

func intList(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}

func decimal(value float64) string {
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func number(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func markdownText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	replacer := strings.NewReplacer(
		"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_",
		"[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "|", "\\|",
	)
	return replacer.Replace(value)
}
