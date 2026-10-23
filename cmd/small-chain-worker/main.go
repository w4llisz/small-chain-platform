// Command small-chain-worker runs the trusted PostgreSQL-backed checksum worker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/w4llisz/small-chain-platform/internal/dbworker"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(os.Args[1:], logger); err != nil {
		logger.Error("worker.exit", "error", err)
		os.Exit(1)
	}
}

func run(args []string, logger *slog.Logger) error {
	flags := flag.NewFlagSet("small-chain-worker", flag.ContinueOnError)
	owner := flags.String("owner", "", "unique stable worker identity (required)")
	concurrency := flags.Int("concurrency", 4, "maximum concurrent attempts")
	poll := flags.Duration("poll-interval", 100*time.Millisecond, "delay between claim polls")
	lease := flags.Duration("lease-duration", 10*time.Second, "database lease duration")
	heartbeat := flags.Duration("heartbeat-interval", 3*time.Second, "active-attempt heartbeat interval")
	retryBase := flags.Duration("retry-base", 250*time.Millisecond, "initial retry delay")
	retryMax := flags.Duration("retry-max", 5*time.Second, "maximum retry delay")
	recovery := flags.Duration("recovery-interval", time.Second, "expired-lease sweep interval")
	recoveryBatch := flags.Int("recovery-batch", 100, "maximum leases recovered per sweep")
	metricsAddr := flags.String("metrics-addr", "", "optional worker metrics listen address (for example 127.0.0.1:9091)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *owner == "" {
		return errors.New("owner is required and must be unique per live process")
	}
	cfg := dbworker.Config{
		Owner: *owner, Concurrency: *concurrency, PollInterval: *poll,
		LeaseDuration: *lease, HeartbeatInterval: *heartbeat,
		RetryBase: *retryBase, RetryMax: *retryMax,
		RecoveryInterval: *recovery, RecoveryBatch: *recoveryBatch,
	}
	if err := dbworker.ValidateConfig(cfg); err != nil {
		return err
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := postgres.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()
	worker, err := dbworker.New(cfg, store, jobs.Checksum, logger)
	if err != nil {
		return err
	}
	var (
		metricsServer *http.Server
		metricsErr    <-chan error
	)
	if *metricsAddr != "" {
		listener, err := net.Listen("tcp", *metricsAddr)
		if err != nil {
			return fmt.Errorf("metrics listen: %w", err)
		}
		metricsServer = &http.Server{
			Handler:           metricsHandler(worker),
			ReadHeaderTimeout: 2 * time.Second,
			ReadTimeout:       2 * time.Second,
			WriteTimeout:      2 * time.Second,
			IdleTimeout:       30 * time.Second,
			MaxHeaderBytes:    8 << 10,
		}
		serveErr := make(chan error, 1)
		metricsErr = serveErr
		go func() { serveErr <- metricsServer.Serve(listener) }()
		logger.Info("worker.metrics_started", "address", listener.Addr().String())
	}
	logger.Info("worker.started", "owner", cfg.Owner, "concurrency", cfg.Concurrency,
		"lease_duration", cfg.LeaseDuration, "heartbeat_interval", cfg.HeartbeatInterval)
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()
	var workerErr error
	if metricsErr == nil {
		workerErr = <-workerDone
	} else {
		select {
		case workerErr = <-workerDone:
		case err := <-metricsErr:
			if !errors.Is(err, http.ErrServerClosed) {
				workerErr = fmt.Errorf("serve worker metrics: %w", err)
			}
			stop()
			workerErr = errors.Join(workerErr, <-workerDone)
		}
	}
	if metricsServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		shutdownErr := metricsServer.Shutdown(shutdownCtx)
		shutdownCancel()
		workerErr = errors.Join(workerErr, shutdownErr)
	}
	if workerErr != nil {
		return workerErr
	}
	logger.Info("worker.stopped", "owner", cfg.Owner)
	return nil
}

func metricsHandler(worker *dbworker.Worker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = response.Write([]byte(worker.PrometheusMetrics()))
	})
	return mux
}
