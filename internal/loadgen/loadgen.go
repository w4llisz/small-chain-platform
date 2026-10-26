// Package loadgen runs a bounded HTTP admission workload against Small Chain.
package loadgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion   = 1
	maxRequests     = 100_000
	maxConcurrency  = 1_000
	maxResponseBody = 4 << 10
)

var latencyBounds = [...]time.Duration{
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
	60 * time.Second,
}

// Config defines one finite workload. Every request uses a unique deterministic
// idempotency key and the same normalized task specification.
type Config struct {
	BaseURL           string
	Requests          int
	Concurrency       int
	RequestTimeout    time.Duration
	KeyPrefix         string
	Payload           string
	DelayMS           int
	FailFirstAttempts int
	MaxAttempts       int
	TimeoutMS         int
}

// Validate rejects configurations that could create an unbounded or invalid
// workload before any request is sent.
func (c Config) Validate() error {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("url must be an absolute http or https origin")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("url must not contain credentials, a path, query, or fragment")
	}
	if c.Requests < 1 || c.Requests > maxRequests {
		return fmt.Errorf("requests must be between 1 and %d", maxRequests)
	}
	if c.Concurrency < 1 || c.Concurrency > maxConcurrency || c.Concurrency > c.Requests {
		return fmt.Errorf("concurrency must be between 1 and min(requests, %d)", maxConcurrency)
	}
	if c.RequestTimeout < 10*time.Millisecond || c.RequestTimeout > 60*time.Second {
		return errors.New("request-timeout must be between 10ms and 60s")
	}
	if len(c.KeyPrefix) < 1 || len(c.KeyPrefix) > 121 {
		return errors.New("key-prefix must be between 1 and 121 ASCII characters")
	}
	for _, r := range c.KeyPrefix {
		if !isKeyRune(r) {
			return errors.New("key-prefix may contain only ASCII letters, digits, '.', '_', ':', or '-'")
		}
	}
	if !utf8.ValidString(c.Payload) || len(c.Payload) > 4096 {
		return errors.New("payload must be valid UTF-8 and at most 4096 bytes")
	}
	if c.DelayMS < 0 || c.DelayMS > 5000 {
		return errors.New("delay-ms must be between 0 and 5000")
	}
	if c.FailFirstAttempts < 0 || c.FailFirstAttempts > 5 {
		return errors.New("fail-first-attempts must be between 0 and 5")
	}
	if c.MaxAttempts < 1 || c.MaxAttempts > 5 {
		return errors.New("max-attempts must be between 1 and 5")
	}
	if c.TimeoutMS < 1 || c.TimeoutMS > 30_000 {
		return errors.New("timeout-ms must be between 1 and 30000")
	}
	return nil
}

func isKeyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)
}

// Doer is implemented by http.Client and permits deterministic transport tests.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Counts classifies every offered admission exactly once. Expected overload and
// capacity responses remain measurements rather than process errors.
type Counts struct {
	Offered          int `json:"offered"`
	Accepted         int `json:"accepted"`
	Replayed         int `json:"replayed"`
	QueueFull        int `json:"queue_full"`
	RecordCapacity   int `json:"record_capacity"`
	Draining         int `json:"draining"`
	OtherClientError int `json:"other_client_error"`
	ServerError      int `json:"server_error"`
	OtherStatus      int `json:"other_status"`
	TransportError   int `json:"transport_error"`
}

// Bucket is a cumulative fixed latency bucket. Bounds are strings so the final
// +Inf bucket has the same stable JSON shape as finite millisecond bounds.
type Bucket struct {
	LEMS  string `json:"le_ms"`
	Count int    `json:"count"`
}

// Latency reports admission round-trip time through response body close. It
// deliberately exposes fixed buckets and a mean, not an inferred percentile.
type Latency struct {
	MinMS   float64  `json:"min_ms"`
	MeanMS  float64  `json:"mean_ms"`
	MaxMS   float64  `json:"max_ms"`
	Buckets []Bucket `json:"cumulative_buckets"`
}

// Workload records the inputs required to interpret a result without echoing
// the payload itself into logs.
type Workload struct {
	Requests          int    `json:"requests"`
	Concurrency       int    `json:"concurrency"`
	RequestTimeoutMS  int64  `json:"request_timeout_ms"`
	KeyPrefix         string `json:"key_prefix"`
	PayloadBytes      int    `json:"payload_bytes"`
	PayloadSHA256     string `json:"payload_sha256"`
	DelayMS           int    `json:"delay_ms"`
	FailFirstAttempts int    `json:"fail_first_attempts"`
	MaxAttempts       int    `json:"max_attempts"`
	TimeoutMS         int    `json:"timeout_ms"`
}

// Summary is the versioned machine-readable output of one completed run.
type Summary struct {
	SchemaVersion int       `json:"schema_version"`
	StartedAt     time.Time `json:"started_at"`
	Target        string    `json:"target"`
	DurationMS    float64   `json:"duration_ms"`
	Workload      Workload  `json:"workload"`
	Counts        Counts    `json:"counts"`
	Latency       Latency   `json:"admission_latency"`
}

type taskSpec struct {
	Kind              string `json:"kind"`
	Payload           string `json:"payload"`
	DelayMS           int    `json:"delay_ms"`
	FailFirstAttempts int    `json:"fail_first_attempts"`
	MaxAttempts       int    `json:"max_attempts"`
	TimeoutMS         int    `json:"timeout_ms"`
}

type partial struct {
	counts  Counts
	count   int
	total   time.Duration
	min     time.Duration
	max     time.Duration
	buckets [len(latencyBounds) + 1]int
}

// Run sends exactly Config.Requests admissions unless configuration or request
// construction fails. It never retries: overload and ambiguous transport
// outcomes stay visible in the report.
func Run(ctx context.Context, client Doer, config Config) (Summary, error) {
	if err := config.Validate(); err != nil {
		return Summary{}, err
	}
	if client == nil {
		return Summary{}, errors.New("http client is required")
	}

	body, err := json.Marshal(taskSpec{
		Kind:              "demo.checksum",
		Payload:           config.Payload,
		DelayMS:           config.DelayMS,
		FailFirstAttempts: config.FailFirstAttempts,
		MaxAttempts:       config.MaxAttempts,
		TimeoutMS:         config.TimeoutMS,
	})
	if err != nil {
		return Summary{}, fmt.Errorf("encode task: %w", err)
	}

	target := strings.TrimRight(config.BaseURL, "/") + "/v1/jobs"
	startTime := time.Now()
	started := startTime.UTC()
	partials := make(chan partial, config.Concurrency)
	var next atomic.Int64
	var workers sync.WaitGroup
	workers.Add(config.Concurrency)

	for range config.Concurrency {
		go func() {
			defer workers.Done()
			var local partial
			for {
				index := int(next.Add(1))
				if index > config.Requests {
					break
				}
				outcome, elapsed := submit(ctx, client, target, body, config, index)
				local.observe(outcome, elapsed)
			}
			partials <- local
		}()
	}

	go func() {
		workers.Wait()
		close(partials)
	}()

	var combined partial
	for item := range partials {
		combined.merge(item)
	}
	summary := combined.summary(config, target, started, time.Since(startTime))
	return summary, ctx.Err()
}

type outcome int

const (
	outcomeAccepted outcome = iota
	outcomeReplayed
	outcomeQueueFull
	outcomeRecordCapacity
	outcomeDraining
	outcomeOtherClient
	outcomeServer
	outcomeOtherStatus
	outcomeTransport
)

func submit(ctx context.Context, client Doer, target string, body []byte, config Config, index int) (outcome, time.Duration) {
	requestContext, cancel := context.WithTimeout(ctx, config.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return outcomeTransport, 0
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", fmt.Sprintf("%s-%06d", config.KeyPrefix, index))

	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return outcomeTransport, time.Since(start)
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	closeErr := response.Body.Close()
	elapsed := time.Since(start)
	if readErr != nil || closeErr != nil {
		return outcomeTransport, elapsed
	}
	return classify(response.StatusCode, responseBody), elapsed
}

func classify(status int, body []byte) outcome {
	switch status {
	case http.StatusAccepted:
		return outcomeAccepted
	case http.StatusOK:
		return outcomeReplayed
	case http.StatusTooManyRequests:
		return outcomeQueueFull
	case http.StatusServiceUnavailable:
		var response struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &response)
		switch response.Error.Code {
		case "record_capacity":
			return outcomeRecordCapacity
		case "draining":
			return outcomeDraining
		default:
			return outcomeServer
		}
	default:
		switch {
		case status >= 400 && status < 500:
			return outcomeOtherClient
		case status >= 500 && status < 600:
			return outcomeServer
		default:
			return outcomeOtherStatus
		}
	}
}

func (p *partial) observe(result outcome, elapsed time.Duration) {
	p.counts.Offered++
	switch result {
	case outcomeAccepted:
		p.counts.Accepted++
	case outcomeReplayed:
		p.counts.Replayed++
	case outcomeQueueFull:
		p.counts.QueueFull++
	case outcomeRecordCapacity:
		p.counts.RecordCapacity++
	case outcomeDraining:
		p.counts.Draining++
	case outcomeOtherClient:
		p.counts.OtherClientError++
	case outcomeServer:
		p.counts.ServerError++
	case outcomeOtherStatus:
		p.counts.OtherStatus++
	case outcomeTransport:
		p.counts.TransportError++
	}
	p.count++
	p.total += elapsed
	if p.count == 1 || elapsed < p.min {
		p.min = elapsed
	}
	if elapsed > p.max {
		p.max = elapsed
	}
	for i, bound := range latencyBounds {
		if elapsed <= bound {
			p.buckets[i]++
		}
	}
	p.buckets[len(latencyBounds)]++
}

func (p *partial) merge(other partial) {
	p.counts.Offered += other.counts.Offered
	p.counts.Accepted += other.counts.Accepted
	p.counts.Replayed += other.counts.Replayed
	p.counts.QueueFull += other.counts.QueueFull
	p.counts.RecordCapacity += other.counts.RecordCapacity
	p.counts.Draining += other.counts.Draining
	p.counts.OtherClientError += other.counts.OtherClientError
	p.counts.ServerError += other.counts.ServerError
	p.counts.OtherStatus += other.counts.OtherStatus
	p.counts.TransportError += other.counts.TransportError
	if other.count > 0 && (p.count == 0 || other.min < p.min) {
		p.min = other.min
	}
	if other.max > p.max {
		p.max = other.max
	}
	p.count += other.count
	p.total += other.total
	for i := range p.buckets {
		p.buckets[i] += other.buckets[i]
	}
}

func (p partial) summary(config Config, target string, started time.Time, duration time.Duration) Summary {
	digest := sha256.Sum256([]byte(config.Payload))
	buckets := make([]Bucket, 0, len(p.buckets))
	for i, bound := range latencyBounds {
		buckets = append(buckets, Bucket{LEMS: fmt.Sprintf("%g", float64(bound)/float64(time.Millisecond)), Count: p.buckets[i]})
	}
	buckets = append(buckets, Bucket{LEMS: "+Inf", Count: p.buckets[len(latencyBounds)]})
	mean := time.Duration(0)
	if p.count > 0 {
		mean = p.total / time.Duration(p.count)
	}
	return Summary{
		SchemaVersion: SchemaVersion,
		StartedAt:     started,
		Target:        target,
		DurationMS:    milliseconds(duration),
		Workload: Workload{
			Requests:          config.Requests,
			Concurrency:       config.Concurrency,
			RequestTimeoutMS:  config.RequestTimeout.Milliseconds(),
			KeyPrefix:         config.KeyPrefix,
			PayloadBytes:      len(config.Payload),
			PayloadSHA256:     hex.EncodeToString(digest[:]),
			DelayMS:           config.DelayMS,
			FailFirstAttempts: config.FailFirstAttempts,
			MaxAttempts:       config.MaxAttempts,
			TimeoutMS:         config.TimeoutMS,
		},
		Counts: p.counts,
		Latency: Latency{
			MinMS:   milliseconds(p.min),
			MeanMS:  milliseconds(mean),
			MaxMS:   milliseconds(p.max),
			Buckets: buckets,
		},
	}
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration.Round(time.Microsecond)) / float64(time.Millisecond)
}
