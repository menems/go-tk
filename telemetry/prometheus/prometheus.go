// Package prometheus lets a Prometheus server scrape OpenTelemetry metrics.
//
// Reader plugs into go-tk/telemetry/otel as a metric reader, and Handler
// serves what it collected on the route Prometheus scrapes:
//
//	reg := promclient.NewRegistry()
//	reader, err := prometheus.Reader(reg)
//	tel, err := otel.Setup(ctx, otel.Config{
//	    ServiceName:   "users",
//	    MetricReaders: []sdkmetric.Reader{reader},
//	})
//	mux.Handle("GET /metrics", prometheus.Handler(reg))
//
// The instruments stay OpenTelemetry's, so a service switches between scraping
// and pushing by changing this wiring in main and nothing else. A push-based
// service needs none of this package, nor its dependency.
//
// The package name shadows github.com/prometheus/client_golang/prometheus, so
// a main that needs both aliases one of them.
package prometheus

import (
	"fmt"
	"net/http"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Reader returns a pull-based metric reader that records into reg, to be
// passed as one of otel.Config's MetricReaders.
func Reader(reg promclient.Registerer) (sdkmetric.Reader, error) {
	reader, err := promexporter.New(promexporter.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("telemetry: prometheus reader: %w", err)
	}
	return reader, nil
}

// Handler serves a registry, for the /metrics route a scraped service
// exposes.
func Handler(g promclient.Gatherer) http.Handler {
	return promhttp.HandlerFor(g, promhttp.HandlerOpts{})
}
