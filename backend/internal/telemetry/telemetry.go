// Package telemetry starts the OpenTelemetry SDK and stops it again.
//
// The backend never runs without telemetry: the settings come from the five
// variables of k8s/README.md section 5, and the process refuses to start when
// one is missing. What this package does not do is fail because the Collector is
// absent — the exporter connects lazily and retries on its own, so a Collector
// that is down delays a trace and never a run.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// loggerName names this instrumentation on every log record it carries.
const loggerName = "gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend"

// ShutdownTimeout bounds the flush at exit. A pod is given time to stop, and the
// last trace of the last run is worth a few seconds of it.
const ShutdownTimeout = 5 * time.Second

// MetricInterval is how often the metrics leave the process.
//
// It is well under the scrape interval Prometheus will use, because a metric
// that arrives late arrives after the trace it points at, and an exemplar whose
// trace is not in Tempo yet reads as a broken drilldown to whoever clicks it.
const MetricInterval = 15 * time.Second

// Shutdown flushes what is pending and closes the exporter. main defers it.
type Shutdown func(context.Context) error

// Start builds the three providers and installs them globally, so that any
// package can reach them with otel.Tracer and otel.Meter without being handed
// one. The logger is the exception: it is returned, because a log line only
// carries a trace identifier when it is written through this one.
//
// serviceName must be the value of OTEL_SERVICE_NAME. The contract fixes it to
// lgtm-backend, and the Tempo datasource of #27 queries that same text — when
// the two differ, "Logs for this span" returns nothing and Grafana reports no
// error at all.
func Start(ctx context.Context, serviceName, endpoint string) (*slog.Logger, Shutdown, error) {
	attributes, err := describe(serviceName)
	if err != nil {
		return nil, nil, err
	}

	stopTracing, err := startTracing(ctx, endpoint, attributes)
	if err != nil {
		return nil, nil, err
	}

	stopMetrics, err := startMetrics(ctx, endpoint, attributes)
	if err != nil {
		// The tracer provider is already installed and already holds a
		// connection. A start that fails halfway and leaves one behind is a leak
		// no one goes looking for.
		return nil, nil, errors.Join(err, stopTracing(ctx))
	}

	stopLogging, err := startLogging(ctx, endpoint, attributes)
	if err != nil {
		return nil, nil, errors.Join(err, stopTracing(ctx), stopMetrics(ctx))
	}

	// tracecontext is what carries a trace across a process boundary. It is what
	// lets Kubo's gateway spans join ours in the bonus, and it costs nothing here.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	shutdown := func(ctx context.Context) error {
		// All three, always, and every error kept. A flush that half worked is not
		// a flush that worked, and the half that failed is the one worth naming.
		return errors.Join(stopTracing(ctx), stopMetrics(ctx), stopLogging(ctx))
	}
	return otelslog.NewLogger(loggerName), shutdown, nil
}

// describe names this service on every span and every metric it produces.
func describe(serviceName string) (*resource.Resource, error) {
	// NewSchemaless, not NewWithAttributes: resource.Merge refuses to join two
	// resources that name different schema versions, and the default resource
	// carries whichever version the SDK was built against. Pinning our own here
	// makes the process refuse to start the day the SDK moves — at run time, on
	// a version bump, with nothing failing at compile time to warn us.
	//
	// A schemaless resource has no version to disagree about, and the merge keeps
	// the one the SDK already declared.
	attributes, err := resource.Merge(resource.Default(),
		resource.NewSchemaless(semconv.ServiceName(serviceName)))
	if err != nil {
		return nil, fmt.Errorf("could not describe this service: %w", err)
	}
	return attributes, nil
}

func startTracing(ctx context.Context, endpoint string,
	attributes *resource.Resource) (Shutdown, error) {
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpointURL(endpoint),
		// The Collector sits inside the cluster, on a network the NetworkPolicy of
		// #22 already limits. There is no certificate to check on that hop and no
		// one to hide it from.
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("could not build the trace exporter for %s: %w", endpoint, err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(attributes),
	)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}

// startMetrics installs the meter provider the three instruments of section 6
// are recorded on.
//
// Nothing here configures the exemplar filter. The SDK reads
// OTEL_METRICS_EXEMPLAR_FILTER from the environment itself, and the config
// package has already refused to start on any value but trace_based — so by the
// time this runs, the filter that attaches a trace identifier to the histogram
// is the only one that can be in force.
func startMetrics(ctx context.Context, endpoint string,
	attributes *resource.Resource) (Shutdown, error) {
	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpointURL(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("could not build the metric exporter for %s: %w", endpoint, err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(MetricInterval))),
		sdkmetric.WithResource(attributes),
	)
	otel.SetMeterProvider(provider)
	return provider.Shutdown, nil
}

// startLogging installs the logger provider behind the slog bridge.
//
// This is the whole of "Logs for this span": Loki reads the trace identifier out
// of an OTLP log record and stores it as structured metadata, and Grafana then
// matches it against the span. A line written to standard output instead carries
// no identifier at all, and the button returns nothing while reporting no error.
func startLogging(ctx context.Context, endpoint string,
	attributes *resource.Resource) (Shutdown, error) {
	exporter, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpointURL(endpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("could not build the log exporter for %s: %w", endpoint, err)
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(attributes),
	)
	global.SetLoggerProvider(provider)
	return provider.Shutdown, nil
}
