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

	"github.com/w4llisz/small-chain-platform/internal/httpapi"
	"github.com/w4llisz/small-chain-platform/internal/jobs"
	"github.com/w4llisz/small-chain-platform/internal/maintenance"
	"github.com/w4llisz/small-chain-platform/internal/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("service.exit", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address (local demo; no authentication)")
	storage := flag.String("storage", "memory", "storage backend: memory or postgres")
	workers := flag.Int("workers", 4, "concurrent worker slots")
	queue := flag.Int("queue", 64, "pending queue capacity")
	maxJobs := flag.Int("max-jobs", 10000, "maximum retained records and idempotency keys")
	grace := flag.Duration("shutdown-grace", 10*time.Second, "total graceful shutdown budget")
	retentionInterval := flag.Duration("retention-interval", time.Minute, "PostgreSQL terminal cleanup cadence; 0 disables cleanup")
	retentionBatch := flag.Int("retention-batch", 100, "maximum terminal jobs deleted per cleanup sweep")
	flag.Parse()
	if *grace <= 0 {
		return errors.New("shutdown-grace must be positive")
	}
	var (
		api       *httpapi.API
		engine    *jobs.Engine
		pgStore   *postgres.Store
		retention *maintenance.Retention
		err       error
	)
	switch *storage {
	case "memory":
		engine, err = jobs.New(jobs.Config{Workers: *workers, QueueCapacity: *queue, MaxJobs: *maxJobs, RetryBase: 100 * time.Millisecond, RetryMax: 2 * time.Second}, jobs.Checksum, logger)
		if err != nil {
			return err
		}
		api = httpapi.New(engine, logger)
	case "postgres":
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return errors.New("DATABASE_URL is required for postgres storage")
		}
		if *retentionInterval < 0 {
			return errors.New("retention-interval must not be negative")
		}
		pgStore, err = postgres.Open(context.Background(), dsn)
		if err != nil {
			return err
		}
		api = httpapi.NewPostgres(pgStore, logger)
		if *retentionInterval > 0 {
			retention, err = maintenance.NewRetention(pgStore, logger, *retentionInterval, *retentionBatch)
			if err != nil {
				pgStore.Close()
				return err
			}
		}
	default:
		return errors.New("storage must be memory or postgres")
	}
	defer func() {
		if engine != nil {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_ = engine.Shutdown(ctx)
		}
		if pgStore != nil {
			pgStore.Close()
		}
	}()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	var (
		retentionCancel context.CancelFunc
		retentionDone   <-chan struct{}
	)
	if retention != nil {
		retentionCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		retentionCancel = cancel
		retentionDone = done
		go func() {
			retention.Run(retentionCtx)
			close(done)
		}()
		defer func() {
			cancel()
			<-done
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	startedFields := []any{"address", listener.Addr().String(), "storage", *storage}
	if engine != nil {
		startedFields = append(startedFields, "workers", *workers, "queue_capacity", *queue)
	}
	if retention != nil {
		startedFields = append(startedFields, "retention_interval", retentionInterval.String(), "retention_batch", *retentionBatch)
	}
	logger.Info("service.started", startedFields...)
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	api.StopAdmission()
	logger.Info("service.draining")
	deadline, cancel := context.WithTimeout(context.Background(), *grace)
	defer cancel()
	if retentionCancel != nil {
		retentionCancel()
		select {
		case <-retentionDone:
		case <-deadline.Done():
		}
	}
	httpErr := server.Shutdown(deadline)
	if httpErr != nil {
		_ = server.Close()
	}
	var jobErr error
	if engine != nil {
		jobErr = engine.Shutdown(deadline)
	}
	logger.Info("service.stopped")
	return errors.Join(httpErr, jobErr)
}
