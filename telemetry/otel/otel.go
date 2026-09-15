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
// Metrics leave through the readers Config names. For a service scraped rather
// than pushed, go-tk/telemetry/prometheus returns one.
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

// Config describes what to report and where.
type Config struct {
	// ServiceName labels every span and metric. Required: telemetry that
	// does not say which service produced it is not telemetry.
	ServiceName string

	// ServiceVersion labels them too, when known.
	ServiceVersion string

	// OTLPEndpoint is the collector's base URL, such as
	// http://localhost:4318. Empty installs providers with no exporter, so
	// the code under them runs unchanged in a test or on a laptop.
	OTLPEndpoint string

	// MetricReaders are read on top of any OTLP one, for a service scraped
	// rather than pushed. go-tk/telemetry/prometheus returns one.
	MetricReaders []sdkmetric.Reader
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
// The caller shuts them down; a Setup that returned an error installed
// nothing.
func Setup(ctx context.Context, cfg Config) (*Providers, error) {
	if cfg.ServiceName == "" {
		return nil, errors.New("telemetry: ServiceName is required")
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

func newResource(cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
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

func newTracerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}

	if cfg.OTLPEndpoint != "" {
		exp, err := otlptrace.New(ctx, otlptrace.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	return sdktrace.NewTracerProvider(opts...), nil
}

func newMeterProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}

	if cfg.OTLPEndpoint != "" {
		exp, err := otlpmetric.New(ctx, otlpmetric.WithEndpointURL(cfg.OTLPEndpoint))
		if err != nil {
			return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)))
	}

	for _, reader := range cfg.MetricReaders {
		opts = append(opts, sdkmetric.WithReader(reader))
	}

	return sdkmetric.NewMeterProvider(opts...), nil
}
