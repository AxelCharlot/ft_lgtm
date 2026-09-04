package run

import (
	"context"
	"slices"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/api"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/compile"
)

// measured runs one pipeline under a sampled root span and returns what the
// three instruments recorded, with the trace identifier of that run.
//
// The span is sampled on purpose. trace_based is the filter the contract fixes,
// and under it an exemplar is attached only beneath a span that is sampled — so
// a test that ran without one would pass while proving nothing.
func measured(t *testing.T, pipeline *Pipeline, code string) (metricdata.ResourceMetrics,
	trace.TraceID) {
	t.Helper()

	// The SDK reads this at construction, not at record time. Setting it here
	// says out loud which filter this test is about, instead of leaning on a
	// default that a version bump is free to change.
	t.Setenv("OTEL_METRICS_EXEMPLAR_FILTER", "trace_based")

	reader := sdkmetric.NewManualReader()
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previousMeters := otel.GetMeterProvider()
	otel.SetMeterProvider(meters)
	t.Cleanup(func() { otel.SetMeterProvider(previousMeters) })

	instruments, err := NewMetrics()
	if err != nil {
		t.Fatalf("NewMetrics returned %v, want no error", err)
	}
	pipeline.Metrics = instruments

	tracers := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	previousTracers := otel.GetTracerProvider()
	otel.SetTracerProvider(tracers)
	t.Cleanup(func() { otel.SetTracerProvider(previousTracers) })

	ctx, root := tracers.Tracer("test").Start(context.Background(), api.RootSpanName)
	pipeline.Run(ctx, code)
	identifier := root.SpanContext().TraceID()
	root.End()

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect returned %v, want no error", err)
	}
	return collected, identifier
}

// instrumentNamed returns the one instrument that carries this name, and fails
// the test when it is absent. A dashboard reads a name and nothing else.
func instrumentNamed(t *testing.T, collected metricdata.ResourceMetrics,
	name string) metricdata.Metrics {
	t.Helper()

	var seen []string
	for _, scope := range collected.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name == name {
				return instrument
			}
			seen = append(seen, instrument.Name)
		}
	}
	t.Fatalf("no instrument is named %q, the run recorded %v", name, seen)
	return metricdata.Metrics{}
}

// Section 6 of the contract: three instruments, and the two durations in
// seconds. The unit is what makes the Collector append _seconds to the name the
// panels then query, so a missing unit is a renamed metric.
func TestARunRecordsTheThreeInstruments(t *testing.T) {
	collected, _ := measured(t, successfulPipeline(), "fn main() {}")

	for _, want := range []struct {
		name string
		unit string
	}{
		{executionsInstrument, ""},
		{durationInstrument, "s"},
		{lastDurationInstrument, "s"},
	} {
		instrument := instrumentNamed(t, collected, want.name)
		if instrument.Unit != want.unit {
			t.Errorf("%s has the unit %q, want %q", want.name, instrument.Unit, want.unit)
		}
	}
}

// The counter splits on result, and on nothing else. Every other attribute the
// run could carry has too many values and would make a time series per run.
func TestTheCounterSplitsOnTheResult(t *testing.T) {
	refused := &Pipeline{
		Compiler: fakeCompiler{err: &compile.FailedError{Output: "no"}},
	}

	cases := map[string]struct {
		pipeline *Pipeline
		want     string
	}{
		"a run that answered":        {successfulPipeline(), resultSuccess},
		"a run that did not compile": {refused, resultFailure},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			collected, _ := measured(t, c.pipeline, "fn main() {}")

			instrument := instrumentNamed(t, collected, executionsInstrument)
			sum, ok := instrument.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is a %T, want a counter", executionsInstrument, instrument.Data)
			}
			if len(sum.DataPoints) != 1 {
				t.Fatalf("the counter has %d data points, want one", len(sum.DataPoints))
			}

			point := sum.DataPoints[0]
			if point.Value != 1 {
				t.Errorf("the counter is %d, want 1 for one run", point.Value)
			}
			if point.Attributes.Len() != 1 {
				t.Errorf("the counter carries %v, want the result and nothing else",
					point.Attributes.ToSlice())
			}
			got, present := point.Attributes.Value(attributeResult)
			if !present {
				t.Fatalf("the counter carries no %s attribute", attributeResult)
			}
			if got.AsString() != c.want {
				t.Errorf("result is %q, want %q", got.AsString(), c.want)
			}
		})
	}
}

// The one test gate B rests on. The histogram is the only instrument that
// carries exemplars, and an exemplar with no trace identifier is a graph point
// that leads nowhere. This fails the moment the histogram is recorded outside
// the span, which is the one mistake that costs nothing at compile time and
// everything at the demonstration.
func TestTheHistogramCarriesAnExemplarHoldingTheTraceIdentifier(t *testing.T) {
	collected, identifier := measured(t, successfulPipeline(), "fn main() {}")

	instrument := instrumentNamed(t, collected, durationInstrument)
	histogram, ok := instrument.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s is a %T, want a histogram", durationInstrument, instrument.Data)
	}
	if len(histogram.DataPoints) != 1 {
		t.Fatalf("the histogram has %d data points, want one", len(histogram.DataPoints))
	}

	point := histogram.DataPoints[0]
	if point.Count != 1 {
		t.Errorf("the histogram counted %d runs, want 1", point.Count)
	}
	if len(point.Exemplars) == 0 {
		t.Fatal("the histogram carries no exemplar, so no graph point can open a trace")
	}

	exemplar := point.Exemplars[0]
	var got trace.TraceID
	copy(got[:], exemplar.TraceID)
	if got != identifier {
		t.Errorf("the exemplar names the trace %s, want %s of this run", got, identifier)
	}
}

// The gauge answers "how long was the last run", which is what makes
// max_over_time honest on a dashboard.
func TestTheGaugeHoldsTheDurationOfTheLastRun(t *testing.T) {
	collected, _ := measured(t, successfulPipeline(), "fn main() {}")

	instrument := instrumentNamed(t, collected, lastDurationInstrument)
	gauge, ok := instrument.Data.(metricdata.Gauge[float64])
	if !ok {
		t.Fatalf("%s is a %T, want a gauge", lastDurationInstrument, instrument.Data)
	}
	if len(gauge.DataPoints) != 1 {
		t.Fatalf("the gauge has %d data points, want one", len(gauge.DataPoints))
	}
	if gauge.DataPoints[0].Value < 0 {
		t.Errorf("the gauge holds %v seconds", gauge.DataPoints[0].Value)
	}
}

// Every test of the stages builds a Pipeline with no Metrics. A nil one has to
// stay a pipeline that runs, or this issue breaks every test that came before.
func TestAPipelineWithNoMetricsStillRuns(t *testing.T) {
	pipeline := successfulPipeline()
	pipeline.Metrics = nil

	answer := pipeline.Run(context.Background(), "fn main() {}")
	if answer.Error != nil {
		t.Fatalf("the run answered %v, want no error", answer.Error)
	}
}

// The boundaries decide whether histogram_quantile can answer anything at all.
// The defaults of the SDK are milliseconds — 0, 5, 10, 25 up to 10000 — and
// under them every run of this playground falls into the first bucket. The p95
// panel of dashboard 2 would draw a flat line at five seconds and nothing would
// look broken.
func TestTheHistogramIsBoundedInSeconds(t *testing.T) {
	collected, _ := measured(t, successfulPipeline(), "fn main() {}")

	instrument := instrumentNamed(t, collected, durationInstrument)
	histogram, ok := instrument.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s is a %T, want a histogram", durationInstrument, instrument.Data)
	}
	if len(histogram.DataPoints) != 1 {
		t.Fatalf("the histogram has %d data points, want one", len(histogram.DataPoints))
	}

	got := histogram.DataPoints[0].Bounds
	if !slices.Equal(got, durationBuckets) {
		t.Errorf("the boundaries are %v, want %v", got, durationBuckets)
	}
}
