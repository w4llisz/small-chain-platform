package dbworker

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var attemptDurationBuckets = [...]time.Duration{
	5 * time.Millisecond,
	10 * time.Millisecond,
	25 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2500 * time.Millisecond,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
}

// AttemptMetricsSnapshot is one consistent view of completed executor calls.
// Bucket counts are cumulative and share indexes with Bounds.
type AttemptMetricsSnapshot struct {
	Bounds           []time.Duration
	CumulativeCounts []uint64
	Count            uint64
	Sum              time.Duration
}

type attemptMetrics struct {
	mu      sync.Mutex
	buckets [len(attemptDurationBuckets)]uint64
	count   uint64
	sum     time.Duration
}

func (m *attemptMetrics) observe(duration time.Duration) {
	if duration < 0 {
		duration = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for index, bound := range attemptDurationBuckets {
		if duration <= bound {
			m.buckets[index]++
		}
	}
	m.count++
	m.sum += duration
}

func (m *attemptMetrics) snapshot() AttemptMetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return AttemptMetricsSnapshot{
		Bounds:           append([]time.Duration(nil), attemptDurationBuckets[:]...),
		CumulativeCounts: append([]uint64(nil), m.buckets[:]...),
		Count:            m.count,
		Sum:              m.sum,
	}
}

func (m *attemptMetrics) prometheus() string {
	snapshot := m.snapshot()
	var output strings.Builder
	output.WriteString("# HELP small_chain_worker_attempt_duration_seconds Wall-clock duration of trusted executor calls; excludes claim, heartbeat and outcome persistence.\n")
	output.WriteString("# TYPE small_chain_worker_attempt_duration_seconds histogram\n")
	for index, bound := range snapshot.Bounds {
		fmt.Fprintf(&output, "small_chain_worker_attempt_duration_seconds_bucket{le=%q} %d\n",
			formatSeconds(bound), snapshot.CumulativeCounts[index])
	}
	fmt.Fprintf(&output, "small_chain_worker_attempt_duration_seconds_bucket{le=\"+Inf\"} %d\n", snapshot.Count)
	fmt.Fprintf(&output, "small_chain_worker_attempt_duration_seconds_sum %s\n", formatSeconds(snapshot.Sum))
	fmt.Fprintf(&output, "small_chain_worker_attempt_duration_seconds_count %d\n", snapshot.Count)
	return output.String()
}

func formatSeconds(duration time.Duration) string {
	return strconv.FormatFloat(duration.Seconds(), 'g', -1, 64)
}
