package otel_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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

// TestSetupPushesEachSignalToItsOwnPath pins that the endpoint is a base URL:
// each signal goes to its OTLP path appended to whatever path the base
// carries, and nothing goes anywhere else.
func TestSetupPushesEachSignalToItsOwnPath(t *testing.T) {
	clearOTLPEnv(t)

	tests := []struct {
		name string
		base string
		want []string
	}{
		{name: "bare host", base: "", want: []string{"/v1/metrics", "/v1/traces"}},
		{name: "base path", base: "/otlp", want: []string{"/otlp/v1/metrics", "/otlp/v1/traces"}},
		{name: "base path with trailing slash", base: "/otlp/", want: []string{"/otlp/v1/metrics", "/otlp/v1/traces"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receiver := newPathRecorder(t)

			p, err := otel.Setup(context.Background(), "svc", otel.WithOTLPEndpoint(receiver.url+tt.base))
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}

			_, span := p.Tracer.Tracer("test").Start(context.Background(), "op")
			span.End()
			counter, err := p.Meter.Meter("test").Int64Counter("requests")
			if err != nil {
				t.Fatalf("Int64Counter: %v", err)
			}
			counter.Add(context.Background(), 1)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}

			if got := receiver.paths(); !slices.Equal(got, tt.want) {
				t.Errorf("receiver got requests on %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSetupKeepsAPasswordOutOfAParseError pins that a base URL that does not
// parse is refused at boot without quoting what it carries.
func TestSetupKeepsAPasswordOutOfAParseError(t *testing.T) {
	_, err := otel.Setup(context.Background(), "svc", otel.WithOTLPEndpoint("http://user:hunter2@collector:port"))
	if err == nil {
		t.Fatal("expected an error for an unparsable endpoint, got nil")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error = %q, want it free of the password", err)
	}
}

// TestSetupRefusesABaseURLNoCollectorIsReachedAt pins that a base URL a
// signal's URL could not be built from is refused at boot, naming what is
// wrong, and that the refused Setup leaves the globals as it found them.
func TestSetupRefusesABaseURLNoCollectorIsReachedAt(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "no scheme", endpoint: "collector:4318/otlp", want: "scheme"},
		{name: "no scheme nor host", endpoint: "/otlp", want: "scheme"},
		{name: "scheme other than http or https", endpoint: "grpc://collector:4317", want: "scheme"},
		{name: "no host", endpoint: "http:///otlp", want: "host"},
		{name: "port without a host", endpoint: "http://:4318", want: "host"},
		{name: "query", endpoint: "http://collector:4318/otlp?tenant=a", want: "query"},
		{name: "empty query", endpoint: "http://collector:4318/otlp?", want: "query"},
		{name: "fragment", endpoint: "http://collector:4318/otlp#top", want: "fragment"},
		{name: "empty fragment", endpoint: "http://collector:4318/otlp#", want: "fragment"},
		{name: "userinfo", endpoint: "http://user@collector:4318", want: "userinfo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			untouched := installSentinelGlobals(t)

			_, err := otel.Setup(context.Background(), "svc", otel.WithOTLPEndpoint(tt.endpoint))
			if err == nil {
				t.Fatalf("expected an error for %q, got nil", tt.endpoint)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to name the %s", err, tt.want)
			}
			untouched(t)
		})
	}
}

// TestSetupKeepsAPasswordOutOfItsError pins that no error Setup returns for
// a base URL quotes the password it carries, whether the URL parses or not.
func TestSetupKeepsAPasswordOutOfItsError(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		password string
	}{
		{name: "userinfo refused", endpoint: "http://user:hunter2@collector:4318", password: "hunter2"},
		{name: "password with a slash", endpoint: "http://user:hunt/er2@collector:4318", password: "hunt"},
		{name: "password with a bad escape", endpoint: "http://user:hu%zznter2@collector:4318", password: "%zz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			untouched := installSentinelGlobals(t)

			_, err := otel.Setup(context.Background(), "svc", otel.WithOTLPEndpoint(tt.endpoint))
			if err == nil {
				t.Fatalf("expected an error for an endpoint carrying a password, got nil")
			}
			if strings.Contains(err.Error(), tt.password) {
				t.Errorf("error = %q, want it free of %q", err, tt.password)
			}
			untouched(t)
		})
	}
}

// installSentinelGlobals sets providers and a propagator of its own as the
// OpenTelemetry globals and returns a check that they are still the globals.
func installSentinelGlobals(t *testing.T) func(*testing.T) {
	t.Helper()

	tracer := sdktrace.NewTracerProvider()
	meter := sdkmetric.NewMeterProvider()
	t.Cleanup(func() {
		_ = tracer.Shutdown(context.Background())
		_ = meter.Shutdown(context.Background())
	})
	otelapi.SetTracerProvider(tracer)
	otelapi.SetMeterProvider(meter)
	otelapi.SetTextMapPropagator(propagation.Baggage{})

	return func(t *testing.T) {
		t.Helper()

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
}

// pathRecorder is a local OTLP receiver that accepts every request and
// records the path it came to.
type pathRecorder struct {
	url string

	mu   sync.Mutex
	seen map[string]bool
}

func newPathRecorder(t *testing.T) *pathRecorder {
	t.Helper()

	r := &pathRecorder{seen: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.seen[req.URL.Path] = true
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	r.url = srv.URL
	return r
}

// paths returns the distinct paths requested, sorted.
func (r *pathRecorder) paths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var got []string
	for path := range r.seen {
		got = append(got, path)
	}
	slices.Sort(got)
	return got
}

// clearOTLPEnv blanks the variables the OTLP exporters read, which the
// exporters treat as unset, so a collector configured on the machine running
// the tests cannot redirect or reshape what they send.
func clearOTLPEnv(t *testing.T) {
	t.Helper()

	for _, signal := range []string{"", "TRACES_", "METRICS_"} {
		for _, key := range []string{"ENDPOINT", "HEADERS", "COMPRESSION", "TIMEOUT", "INSECURE", "CERTIFICATE", "CLIENT_CERTIFICATE", "CLIENT_KEY"} {
			t.Setenv("OTEL_EXPORTER_OTLP_"+signal+key, "")
		}
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
