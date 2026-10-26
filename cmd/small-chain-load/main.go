// Command small-chain-load generates one bounded, deterministic HTTP admission
// workload and writes a versioned JSON summary to stdout.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/loadgen"
)

func main() {
	config := loadgen.Config{}
	flag.StringVar(&config.BaseURL, "url", "http://127.0.0.1:8080", "Small Chain HTTP origin")
	flag.IntVar(&config.Requests, "requests", 100, "total unique submissions (1..100000)")
	flag.IntVar(&config.Concurrency, "concurrency", 4, "parallel submissions (1..min(requests,1000))")
	flag.DurationVar(&config.RequestTimeout, "request-timeout", 5*time.Second, "per-request HTTP deadline (10ms..60s)")
	flag.StringVar(&config.KeyPrefix, "key-prefix", "load", "deterministic idempotency-key prefix")
	flag.StringVar(&config.Payload, "payload", "small-chain-load", "shared checksum payload (not echoed in report)")
	flag.IntVar(&config.DelayMS, "delay-ms", 0, "trusted executor delay for every job (0..5000)")
	flag.IntVar(&config.FailFirstAttempts, "fail-first-attempts", 0, "deterministic transient failures per job (0..5)")
	flag.IntVar(&config.MaxAttempts, "max-attempts", 3, "attempt budget for every job (1..5)")
	flag.IntVar(&config.TimeoutMS, "timeout-ms", 1000, "per-attempt task timeout (1..30000)")
	flag.Parse()
	if err := config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid workload: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = config.Concurrency
	transport.MaxIdleConnsPerHost = config.Concurrency
	transport.MaxConnsPerHost = config.Concurrency
	transport.IdleConnTimeout = 30 * time.Second
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	summary, err := loadgen.Run(ctx, client, config)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if summary.SchemaVersion != 0 {
		if encodeErr := encoder.Encode(summary); encodeErr != nil {
			fmt.Fprintf(os.Stderr, "encode report: %v\n", encodeErr)
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "load failed: %v\n", err)
		os.Exit(1)
	}
}
