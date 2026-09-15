package telemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/menems/got-tk/telemetry"
)

// None of these tests calls t.Parallel: Setup installs the OpenTelemetry
// globals, which are process-wide, so two of them running at once would each
// see the other's providers.

func TestSetupRequiresServiceName(t *testing.T) {
	if _, err := telemetry.Setup(context.Background(), telemetry.Config{}); err == nil {
		t.Fatal("expected an error for an empty ServiceName, got nil")
	}
}

// TestSetupWithoutExporters pins that the no-collector case works: a laptop or
// a test runs the instrumented code unchanged, reporting nowhere.
func TestSetupWithoutExporters(t *testing.T) {
	p := setup(t, telemetry.Config{ServiceName: "svc"})

	if p.Tracer == nil || p.Meter == nil {
		t.Fatal("Setup returned a nil provider")
	}

	// A span and a measurement both go nowhere without failing.
	_, span := p.Tracer.Tracer("test").Start(context.Background(), "op")
	span.End()
}

// TestSetupPrometheusReader is the round trip a scraped service depends on: a
// measurement taken through the meter comes back out of the registry.
func TestSetupPrometheusReader(t *testing.T) {
	reg := prometheus.NewRegistry()
	p := setup(t, telemetry.Config{ServiceName: "svc", PrometheusRegistry: reg})

	counter, err := p.Meter.Meter("test").Int64Counter("requests_total")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 3)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, f := range families {
		if !strings.Contains(f.GetName(), "requests") {
			continue
		}
		if got := f.GetMetric()[0].GetCounter().GetValue(); got != 3 {
			t.Fatalf("counter = %v, want 3", got)
		}
		return
	}
	t.Fatalf("no requests metric in the registry, got %d families", len(families))
}

// TestSetupInstallsThePropagator pins the line every one of the replaced
// versions forgot: without it a trace stops at the first service boundary.
func TestSetupInstallsThePropagator(t *testing.T) {
	setup(t, telemetry.Config{ServiceName: "svc"})

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
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	if got := carrier.Get("traceparent"); !strings.Contains(got, traceID.String()) {
		t.Errorf("traceparent = %q, want it to carry %s", got, traceID)
	}
}

func TestMetricsHandler(t *testing.T) {
	reg := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "hits_total"})
	counter.Inc()
	if err := reg.Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}

	rec := httptest.NewRecorder()
	telemetry.MetricsHandler(reg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "hits_total 1") {
		t.Errorf("body = %q, want it to carry hits_total 1", rec.Body.String())
	}
}

// setup runs Setup and registers its shutdown, so no test leaves a provider
// flushing behind it.
func setup(t *testing.T, cfg telemetry.Config) *telemetry.Providers {
	t.Helper()

	p, err := telemetry.Setup(context.Background(), cfg)
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
