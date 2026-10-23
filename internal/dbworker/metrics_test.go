package dbworker

import (
	"strings"
	"testing"
	"time"
)

func TestAttemptMetricsUsesFixedCumulativeBuckets(t *testing.T) {
	var metrics attemptMetrics
	metrics.observe(5 * time.Millisecond)
	metrics.observe(6 * time.Millisecond)
	metrics.observe(31 * time.Second)

	snapshot := metrics.snapshot()
	if snapshot.Count != 3 || snapshot.Sum != 31*time.Second+11*time.Millisecond {
		t.Fatalf("count=%d sum=%s", snapshot.Count, snapshot.Sum)
	}
	if len(snapshot.Bounds) != len(attemptDurationBuckets) || len(snapshot.CumulativeCounts) != len(attemptDurationBuckets) {
		t.Fatalf("bounds=%d counts=%d", len(snapshot.Bounds), len(snapshot.CumulativeCounts))
	}
	if snapshot.CumulativeCounts[0] != 1 || snapshot.CumulativeCounts[1] != 2 || snapshot.CumulativeCounts[len(snapshot.CumulativeCounts)-1] != 2 {
		t.Fatalf("cumulative counts = %v", snapshot.CumulativeCounts)
	}

	text := metrics.prometheus()
	for _, want := range []string{
		`small_chain_worker_attempt_duration_seconds_bucket{le="0.005"} 1`,
		`small_chain_worker_attempt_duration_seconds_bucket{le="0.01"} 2`,
		`small_chain_worker_attempt_duration_seconds_bucket{le="30"} 2`,
		`small_chain_worker_attempt_duration_seconds_bucket{le="+Inf"} 3`,
		`small_chain_worker_attempt_duration_seconds_sum 31.011`,
		`small_chain_worker_attempt_duration_seconds_count 3`,
	} {
		if !strings.Contains(text, want+"\n") {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
}
