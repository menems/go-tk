package otel_test

import (
	"context"
	"strings"
	"testing"
	"time"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	"github.com/menems/got-tk/telemetry/otel"
)

// None of these tests calls t.Parallel: Setup installs the OpenTelemetry
// globals, which are process-wide, so two of them running at once would each
// see the other's providers.

func TestSetupRequiresServiceName(t *testing.T) {
	if _, err := otel.Setup(context.Background(), otel.Config{}); err == nil {
		t.Fatal("expected an error for an empty ServiceName, got nil")
	}
}

// TestSetupWithoutExporters pins that the no-collector case works: a laptop or
// a test runs the instrumented code unchanged, reporting nowhere.
func TestSetupWithoutExporters(t *testing.T) {
	p := setup(t, otel.Config{ServiceName: "svc"})

	if p.Tracer == nil || p.Meter == nil {
		t.Fatal("Setup returned a nil provider")
	}

	// A span goes nowhere without failing.
	_, span := p.Tracer.Tracer("test").Start(context.Background(), "op")
	span.End()
}

// TestSetupUsesTheConfiguredReaders pins the seam the prometheus sibling plugs
// into: a measurement taken through the meter reaches every reader Config
// named.
func TestSetupUsesTheConfiguredReaders(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	p := setup(t, otel.Config{
		ServiceName:   "svc",
		MetricReaders: []sdkmetric.Reader{reader},
	})

	counter, err := p.Meter.Meter("test").Int64Counter("requests")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 3)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	got := counterValue(t, collected, "requests")
	if got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
}

// TestSetupResourceNamesTheService pins that every signal says where it came
// from, which is why ServiceName is required.
func TestSetupResourceNamesTheService(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	p := setup(t, otel.Config{
		ServiceName:   "users",
		MetricReaders: []sdkmetric.Reader{reader},
	})

	counter, err := p.Meter.Meter("test").Int64Counter("requests")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 1)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got := collected.Resource.String(); !strings.Contains(got, "service.name=users") {
		t.Errorf("resource = %q, want it to carry service.name=users", got)
	}
}

// TestSetupInstallsThePropagator pins the line every one of the replaced
// versions forgot: without it a trace stops at the first service boundary.
func TestSetupInstallsThePropagator(t *testing.T) {
	setup(t, otel.Config{ServiceName: "svc"})

	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatalf("TraceIDFromHex: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	if err != nil {
		t.Fatalf("SpanIDFromHex: %v", err)
	}

	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	carrier := propagation.MapCarrier{}
	otelapi.GetTextMapPropagator().Inject(ctx, carrier)

	if got := carrier.Get("traceparent"); !strings.Contains(got, traceID.String()) {
		t.Errorf("traceparent = %q, want it to carry %s", got, traceID)
	}
}

func counterValue(t *testing.T, rm metricdata.ResourceMetrics, name string) int64 {
	t.Helper()

	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is a %T, want a Sum[int64]", name, m.Data)
			}
			return sum.DataPoints[0].Value
		}
	}
	t.Fatalf("no metric named %s was collected", name)
	return 0
}

// setup runs Setup and registers its shutdown, so no test leaves a provider
// flushing behind it.
func setup(t *testing.T, cfg otel.Config) *otel.Providers {
	t.Helper()

	p, err := otel.Setup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return p
}
