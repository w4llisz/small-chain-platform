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
	workers := flag.Int("workers", 4, "concurrent worker slots")
	queue := flag.Int("queue", 64, "pending queue capacity")
	maxJobs := flag.Int("max-jobs", 10000, "maximum retained records and idempotency keys")
	grace := flag.Duration("shutdown-grace", 10*time.Second, "total graceful shutdown budget")
	flag.Parse()
	if *grace <= 0 {
		return errors.New("shutdown-grace must be positive")
	}
	engine, err := jobs.New(jobs.Config{Workers: *workers, QueueCapacity: *queue, MaxJobs: *maxJobs, RetryBase: 100 * time.Millisecond, RetryMax: 2 * time.Second}, jobs.Checksum, logger)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = engine.Shutdown(ctx)
	}()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	server := &http.Server{Handler: httpapi.New(engine, logger), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("service.started", "address", listener.Addr().String(), "workers", *workers, "queue_capacity", *queue, "storage", "memory")
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	engine.StopAdmission()
	logger.Info("service.draining")
	deadline, cancel := context.WithTimeout(context.Background(), *grace)
	defer cancel()
	httpErr := server.Shutdown(deadline)
	if httpErr != nil {
		_ = server.Close()
	}
	jobErr := engine.Shutdown(deadline)
	logger.Info("service.stopped")
	return errors.Join(httpErr, jobErr)
}
