// Package telemetry owns the optional OpenTelemetry SDK lifecycle.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationName = "github.com/w4llisz/small-chain-platform"
	maxQueueSize        = 256
	maxExportBatchSize  = 64
	exportTimeout       = 2 * time.Second
	batchTimeout        = time.Second
)

// Config controls an OTLP/HTTP trace exporter. An empty endpoint keeps tracing
// disabled without starting an exporter goroutine.
type Config struct {
	Endpoint    string
	ServiceName string
	SampleRatio float64
}

// Provider is a process-local tracer provider and W3C trace-context propagator.
// It never installs global OpenTelemetry state.
type Provider struct {
	tracerProvider trace.TracerProvider
	shutdown       func(context.Context) error
}

// New constructs a bounded OTLP/HTTP tracing pipeline. Export retry is disabled
// so an unavailable collector cannot create an unbounded retry backlog.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Endpoint == "" {
		return &Provider{
			tracerProvider: trace.NewNoopTracerProvider(),
			shutdown:       func(context.Context) error { return nil },
		}, nil
	}
	if cfg.ServiceName == "" {
		return nil, errors.New("trace service name is required when tracing is enabled")
	}
	if cfg.SampleRatio <= 0 || cfg.SampleRatio > 1 {
		return nil, errors.New("trace sample ratio must be greater than 0 and at most 1")
	}
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(cfg.Endpoint),
		otlptracehttp.WithTimeout(exportTimeout),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", cfg.ServiceName))),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
		sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(maxQueueSize),
			sdktrace.WithMaxExportBatchSize(maxExportBatchSize),
			sdktrace.WithBatchTimeout(batchTimeout),
			sdktrace.WithExportTimeout(exportTimeout),
		),
	)
	return &Provider{tracerProvider: provider, shutdown: provider.Shutdown}, nil
}

// Tracer returns the process tracer. Disabled providers return a no-op tracer.
func (p *Provider) Tracer() trace.Tracer {
	return p.tracerProvider.Tracer(instrumentationName)
}

// Propagator extracts W3C traceparent headers without accepting baggage as
// span attributes.
func (p *Provider) Propagator() propagation.TextMapPropagator {
	return propagation.TraceContext{}
}

// Shutdown flushes the bounded exporter queue until ctx expires.
func (p *Provider) Shutdown(ctx context.Context) error {
	return p.shutdown(ctx)
}

// ShutdownNonFatal flushes traces within a fixed budget. Export failure is
// logged but never changes the service's business-path exit status.
func ShutdownNonFatal(provider *Provider, logger *slog.Logger) {
	if provider == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()
	if err := provider.Shutdown(ctx); err != nil {
		logger.Warn("telemetry.trace_shutdown_failed", "error", err)
	}
}
