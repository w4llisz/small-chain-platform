package telemetry

import (
	"context"
	"testing"
	"time"
)

func TestDisabledProviderIsUsable(t *testing.T) {
	provider, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, span := provider.Tracer().Start(context.Background(), "disabled")
	span.End()
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEnabledProviderValidatesBoundedConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		config Config
	}{
		{name: "service", config: Config{Endpoint: "http://collector:4318/v1/traces", SampleRatio: 1}},
		{name: "zero ratio", config: Config{Endpoint: "http://collector:4318/v1/traces", ServiceName: "api"}},
		{name: "ratio above one", config: Config{Endpoint: "http://collector:4318/v1/traces", ServiceName: "api", SampleRatio: 1.01}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(context.Background(), test.config); err == nil {
				t.Fatal("invalid tracing configuration was accepted")
			}
		})
	}
}

func TestMalformedEndpointRejected(t *testing.T) {
	for _, endpoint := range []string{
		"http://localhost/%synthetic_private_value",
		"synthetic_private_value/v1/traces",
		"ftp://localhost/synthetic_private_value",
		"http:///synthetic_private_value",
		"http://:4318/synthetic_private_value",
	} {
		provider, err := New(context.Background(), Config{Endpoint: endpoint, ServiceName: "api", SampleRatio: 1})
		if provider != nil {
			_ = provider.Shutdown(context.Background())
			t.Fatal("invalid endpoint created provider")
		}
		if err == nil || err.Error() != "trace endpoint must be an absolute HTTP(S) URL with a host" {
			t.Fatal("expected fixed endpoint diagnostic")
		}
	}
}

func TestValidEndpointCreatesProviderWithoutExport(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:4318/v1/traces", "https://localhost/custom", "http://[::1]:4318/v1/traces"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		provider, err := New(ctx, Config{Endpoint: endpoint, ServiceName: "api", SampleRatio: 1})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		// No spans: this tests construction/shutdown without contacting a collector.
		err = provider.Shutdown(ctx)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}
