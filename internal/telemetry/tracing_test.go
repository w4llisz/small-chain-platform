package telemetry

import (
	"context"
	"testing"
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
