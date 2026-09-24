package otel_test

import (
	"context"
	"strings"
	"testing"
	"time"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/menems/go-tk/telemetry/otel"
)

// None of these tests calls t.Parallel: Setup installs the OpenTelemetry
// globals, which are process-wide, so two of them running at once would each
// see the other's providers.

func TestSetupRequiresServiceName(t *testing.T) {
	if _, err := otel.Setup(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an empty service name, got nil")
	}
}

// TestSetupRefusesANilReader pins that an invalid option fails at boot, with
// an error, and that the refused Setup leaves the globals as it found them.
func TestSetupRefusesANilReader(t *testing.T) {
	tracer := sdktrace.NewTracerProvider()
	meter := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = tracer.Shutdown(context.Background())
		_ = meter.Shutdown(context.Background())
	})
	otelapi.SetTracerProvider(tracer)
	otelapi.SetMeterProvider(meter)
	otelapi.SetTextMapPropagator(propagation.Baggage{})

	_, err := otel.Setup(context.Background(), "svc",
		otel.WithMetricReader(sdkmetric.NewManualReader()),
		otel.WithMetricReader(nil),
	)
	if err == nil {
		t.Fatal("expected an error for a nil metric reader, got nil")
	}

	if otelapi.GetTracerProvider() != trace.TracerProvider(tracer) {
		t.Error("a refused Setup replaced the global tracer provider")
	}
	if otelapi.GetMeterProvider() != metric.MeterProvider(meter) {
		t.Error("a refused Setup replaced the global meter provider")
	}
	if otelapi.GetTextMapPropagator() != propagation.TextMapPropagator(propagation.Baggage{}) {
		t.Error("a refused Setup replaced the global propagator")
	}
}

// TestSetupWithoutExporters pins that the no-collector case works: a laptop or
// a test runs the instrumented code unchanged, reporting nowhere.
func TestSetupWithoutExporters(t *testing.T) {
	p := setup(t, "svc")

	if p.Tracer == nil || p.Meter == nil {
		t.Fatal("Setup returned a nil provider")
	}

	// A span goes nowhere without failing.
	_, span := p.Tracer.Tracer("test").Start(context.Background(), "op")
	span.End()
}

// TestSetupUsesEveryReader pins the seam the prometheus sibling plugs into: a
// measurement taken through the meter reaches every reader WithMetricReader
// added, not only the last one.
func TestSetupUsesEveryReader(t *testing.T) {
	first := sdkmetric.NewManualReader()
	second := sdkmetric.NewManualReader()
	p := setup(t, "svc", otel.WithMetricReader(first), otel.WithMetricReader(second))

	counter, err := p.Meter.Meter("test").Int64Counter("requests")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 3)

	for name, reader := range map[string]sdkmetric.Reader{"first": first, "second": second} {
		var collected metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &collected); err != nil {
			t.Fatalf("%s reader: Collect: %v", name, err)
		}
		if got := counterValue(t, collected, "requests"); got != 3 {
			t.Errorf("%s reader: requests = %d, want 3", name, got)
		}
	}
}

// TestSetupResourceNamesTheService pins that every signal says where it came
// from, which is why the service name is required, and which version it ran
// when WithServiceVersion names one.
func TestSetupResourceNamesTheService(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	p := setup(t, "users", otel.WithServiceVersion("1.2.3"), otel.WithMetricReader(reader))

	counter, err := p.Meter.Meter("test").Int64Counter("requests")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 1)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	got := collected.Resource.String()
	for _, want := range []string{"service.name=users", "service.version=1.2.3"} {
		if !strings.Contains(got, want) {
			t.Errorf("resource = %q, want it to carry %s", got, want)
		}
	}
}

// TestSetupInstallsThePropagator pins the line every one of the replaced
// versions forgot: without it a trace stops at the first service boundary.
func TestSetupInstallsThePropagator(t *testing.T) {
	setup(t, "svc")

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
func setup(t *testing.T, serviceName string, opts ...otel.Option) *otel.Providers {
	t.Helper()

	p, err := otel.Setup(context.Background(), serviceName, opts...)
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
