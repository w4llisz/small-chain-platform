// Command small-chain-worker runs the trusted PostgreSQL-backed checksum worker.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
	logger.Info("worker.started", "owner", cfg.Owner, "concurrency", cfg.Concurrency,
		"lease_duration", cfg.LeaseDuration, "heartbeat_interval", cfg.HeartbeatInterval)
	if err := worker.Run(ctx); err != nil {
		return err
	}
	logger.Info("worker.stopped", "owner", cfg.Owner)
	return nil
}
