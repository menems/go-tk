package prometheus_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	promclient "github.com/prometheus/client_golang/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/menems/got-tk/telemetry/prometheus"
)

// TestReaderRoundTrip is the round trip a scraped service depends on: a
// measurement taken through an OpenTelemetry meter comes back out of the
// Prometheus registry.
func TestReaderRoundTrip(t *testing.T) {
	t.Parallel()

	reg := promclient.NewRegistry()
	reader, err := prometheus.Reader(reg)
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}

	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	counter, err := provider.Meter("test").Int64Counter("requests_total")
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

func TestHandler(t *testing.T) {
	t.Parallel()

	reg := promclient.NewRegistry()
	counter := promclient.NewCounter(promclient.CounterOpts{Name: "hits_total"})
	counter.Inc()
	if err := reg.Register(counter); err != nil {
		t.Fatalf("Register: %v", err)
	}

	rec := httptest.NewRecorder()
	prometheus.Handler(reg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "hits_total 1") {
		t.Errorf("body = %q, want it to carry hits_total 1", rec.Body.String())
	}
}
