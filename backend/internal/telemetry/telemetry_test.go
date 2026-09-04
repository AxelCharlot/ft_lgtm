package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const testEndpoint = "http://otel-collector.lgtm.svc.cluster.local:4317"

// Start builds the resource, and building the resource is where two versions of
// the semantic conventions can disagree. That failure appears at run time and
// never at compile time, so it belongs in a test: on 2026-08-29 it reached a pod
// and crash-looped it, because nothing here called Start.
func TestStartInstallsBothProviders(t *testing.T) {
	// The exporters connect lazily, so no Collector has to exist for this.
	logger, flush, err := Start(context.Background(), "lgtm-backend", testEndpoint)
	if err != nil {
		t.Fatalf("Start returned %v, want no error", err)
	}
	if flush == nil {
		t.Fatal("Start returned no shutdown, so nothing would ever be flushed")
	}
	if logger == nil {
		t.Fatal("Start returned no logger, so no line could carry a trace identifier")
	}
	t.Cleanup(func() {
		closing, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := flush(closing); err != nil {
			t.Logf("the shutdown returned %v", err)
		}
	})

	// Both globals, because both are how the rest of the backend reaches them:
	// run.NewMetrics builds its three instruments on otel.Meter, and it would
	// build them on a provider that records nothing without ever saying so.
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); !ok {
		t.Errorf("the global tracer provider is %T, want the SDK one",
			otel.GetTracerProvider())
	}
	if _, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok {
		t.Errorf("the global meter provider is %T, want the SDK one",
			otel.GetMeterProvider())
	}
}

// The meter provider flushes on the way out, and the tracer provider does not.
// With no Collector to answer, that final export fails after the ten seconds the
// exporter waits on its own — so the shutdown has to obey the context main gives
// it, or a rollout holds every stopping pod for those ten seconds.
func TestTheShutdownObeysItsContext(t *testing.T) {
	_, flush, err := Start(context.Background(), "lgtm-backend", testEndpoint)
	if err != nil {
		t.Fatalf("Start returned %v, want no error", err)
	}

	closing, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	started := time.Now()
	// The error is expected here and is not what this test is about: an absent
	// Collector cannot acknowledge the last export. The time it took is.
	if err := flush(closing); err != nil {
		t.Logf("the shutdown returned %v, which is what an absent Collector looks like", err)
	}

	if waited := time.Since(started); waited > 5*time.Second {
		t.Errorf("the shutdown took %v, so it ignored its context", waited)
	}
}
