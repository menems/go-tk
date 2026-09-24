// Package otel wires the OpenTelemetry trace and metric providers at boot and
// flushes them at shutdown.
//
// It deliberately ships no instrumentation. Setup installs the providers as
// OpenTelemetry's globals, which is how otelhttp, otelgrpc and any
// instrumented library find them with no wiring:
//
//	handler := otelhttp.NewHandler(router, "server")
//
// Writing that middleware by hand is how the services this replaces each grew
// a hundred lines reimplementing a maintained library.
//
// Metrics leave through the readers WithMetricReader adds. For a service
// scraped rather than pushed, go-tk/telemetry/prometheus returns one.
//
// Shutdown is not an app.Runner. Telemetry has to outlive the servers it
// observes, or their last spans never leave the process, and an app.App stops
// every runner at once. It belongs in a defer in main, which runs after
// app.Run has returned.
//
// The package name shadows go.opentelemetry.io/otel, so a main that needs
// both aliases one of them.
package otel

import (
	"context"
	"errors"
	"fmt"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otlpmetric "go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otlptrace "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Option is what Setup takes at wiring, on top of the service name.
type Option func(*settings)

// WithServiceVersion labels every span and metric with the version that
// produced it. Empty adds no version label, as omitting the option does.
func WithServiceVersion(version string) Option {
	return func(s *settings) { s.serviceVersion = version }
}

// WithOTLPEndpoint names the collector's base URL, such as
// http://localhost:4318, that spans and metrics are pushed to. Empty installs
// providers with no exporter, as omitting the option does, so the code under
// them runs unchanged in a test or on a laptop, and a caller passes an
// optional config value straight through.
func WithOTLPEndpoint(url string) Option {
	return func(s *settings) { s.otlpEndpoint = url }
}

// WithMetricReader adds a reader, read on top of any OTLP one, for a service
// scraped rather than pushed. go-tk/telemetry/prometheus returns one. Each
// call adds one more, in the order given. A nil reader is refused by Setup.
func WithMetricReader(reader sdkmetric.Reader) Option {
	return func(s *settings) { s.metricReaders = append(s.metricReaders, reader) }
}

type settings struct {
	serviceName    string
	serviceVersion string
	otlpEndpoint   string
	metricReaders  []sdkmetric.Reader
}

// Providers holds what Setup built, for a caller that would rather pass a
// provider explicitly than read the global.
type Providers struct {
	Tracer trace.TracerProvider
	Meter  metric.MeterProvider

	shutdowns []func(context.Context) error
}

// Setup builds the providers, installs them as the OpenTelemetry globals and
// sets the W3C trace context propagator, without which a trace stops at the
// first service boundary.
//
// serviceName labels every span and metric. Setup refuses it empty, since
// telemetry that does not say which service produced it is not telemetry, and
// refuses a nil reader passed to WithMetricReader.
//
// The caller shuts them down; a Setup that returned an error installed
// nothing.
func Setup(ctx context.Context, serviceName string, opts ...Option) (*Providers, error) {
	cfg := settings{serviceName: serviceName}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.serviceName == "" {
		return nil, errors.New("telemetry: service name is required")
	}
	for i, reader := range cfg.metricReaders {
		if reader == nil {
			return nil, fmt.Errorf("telemetry: metric reader %d is nil", i)
		}
	}

	res, err := newResource(cfg)
	if err != nil {
		return nil, err
	}

	tracer, err := newTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}
	meter, err := newMeterProvider(ctx, cfg, res)
	if err != nil {
		// The trace exporter opened a connection; drop it rather than
		// leave it to the garbage collector.
		_ = tracer.Shutdown(ctx)
		return nil, err
	}

	p := &Providers{
		Tracer:    tracer,
		Meter:     meter,
		shutdowns: []func(context.Context) error{tracer.Shutdown, meter.Shutdown},
	}

	otelapi.SetTracerProvider(tracer)
	otelapi.SetMeterProvider(meter)
	otelapi.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return p, nil
}

// Shutdown flushes both providers and returns every error joined. Give it its
// own context with a deadline: the one that stopped the process is already
// cancelled, and a flush needs to outlive it.
func (p *Providers) Shutdown(ctx context.Context) error {
	errs := make([]error, len(p.shutdowns))
	for i, shutdown := range p.shutdowns {
		errs[i] = shutdown(ctx)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("telemetry: shutdown: %w", err)
	}
	return nil
}

func newResource(cfg settings) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.serviceName)}
	if cfg.serviceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.serviceVersion))
	}

	// Schemaless on purpose. Merging two resources that each carry a
	// different non-empty schema URL is an error, so pinning our semconv
	// version against resource.Default()'s would break on every SDK bump
	// that moves it. The attribute keys are the same either way, and the
	// merged resource keeps the SDK's own schema URL.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewSchemaless(attrs...),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	return res, nil
}

func newTracerProvider(ctx context.Context, cfg settings, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}

	if cfg.otlpEndpoint != "" {
		exp, err := otlptrace.New(ctx, otlptrace.WithEndpointURL(cfg.otlpEndpoint))
		if err != nil {
			return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	return sdktrace.NewTracerProvider(opts...), nil
}

func newMeterProvider(ctx context.Context, cfg settings, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}

	if cfg.otlpEndpoint != "" {
		exp, err := otlpmetric.New(ctx, otlpmetric.WithEndpointURL(cfg.otlpEndpoint))
		if err != nil {
			return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
	}

	for _, reader := range cfg.metricReaders {
		opts = append(opts, sdkmetric.WithReader(reader))
	}

	return sdkmetric.NewMeterProvider(opts...), nil
}
