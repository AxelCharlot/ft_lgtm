package run

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName names this instrumentation inside every metric it produces.
const meterName = "gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/run"

// The three instrument names of section 6 of k8s/README.md. These are the names
// in the code; the Collector rewrites them on the way to Prometheus, and section
// 6 holds both halves of that pair.
const (
	executionsInstrument   = "lgtm.executions.total"
	durationInstrument     = "lgtm.execution.duration"
	lastDurationInstrument = "lgtm.execution.duration.last"
)

// attributeResult splits the counter, and it is the only attribute any of the
// three carries. Code text, a CID and a trace identifier all have too many
// possible values, and every one of them would make a new time series.
const attributeResult = attribute.Key("result")

// The two values that attribute takes. A run that answers with an error is a
// failure. A run whose sharing failed is not: the contract is explicit that the
// run succeeded and only the sharing did not.
const (
	resultSuccess = "success"
	resultFailure = "failure"
)

// durationBuckets bounds the histogram, in seconds.
//
// The default boundaries of the SDK start at 0, 5, 10, 25 and climb to 10000,
// which are milliseconds: every run of this playground would land in one bucket
// and histogram_quantile would answer "somewhere under five seconds" forever.
// The panel would still draw, which is what makes it worth writing down. These
// boundaries are seconds, and they are close together where a run actually
// finishes — compile is the slow stage and one run answers in about three.
var durationBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 3, 4, 5, 7.5, 10, 15, 30}

// Metrics holds the three instruments of section 6.
type Metrics struct {
	executions   metric.Int64Counter
	duration     metric.Float64Histogram
	lastDuration metric.Float64Gauge
}

// NewMetrics builds the three instruments on the meter provider that
// telemetry.Start installed.
//
// It is called once, at start. Building an instrument per run would repeat a
// lookup on every request and would put its error where no one reads it.
func NewMetrics() (*Metrics, error) {
	meter := otel.Meter(meterName)

	executions, err := meter.Int64Counter(executionsInstrument,
		metric.WithDescription("How many runs finished, split by result."))
	if err != nil {
		return nil, fmt.Errorf("could not build the counter %s: %w", executionsInstrument, err)
	}

	duration, err := meter.Float64Histogram(durationInstrument,
		metric.WithDescription("How long a whole run took."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	if err != nil {
		return nil, fmt.Errorf("could not build the histogram %s: %w", durationInstrument, err)
	}

	lastDuration, err := meter.Float64Gauge(lastDurationInstrument,
		metric.WithDescription("How long the last run took."),
		metric.WithUnit("s"))
	if err != nil {
		return nil, fmt.Errorf("could not build the gauge %s: %w", lastDurationInstrument, err)
	}

	return &Metrics{
		executions:   executions,
		duration:     duration,
		lastDuration: lastDuration,
	}, nil
}

// Record writes the three instruments for one finished run.
//
// ctx must still carry the span of that run, and that is the whole reason this
// is called before the span ends: the SDK reads the span out of the context and
// attaches its trace identifier to the histogram as an exemplar. Recorded
// outside the span the numbers are identical, every graph looks right, and the
// click from a point to a trace never works.
func (m *Metrics) Record(ctx context.Context, elapsed time.Duration, succeeded bool) {
	result := resultFailure
	if succeeded {
		result = resultSuccess
	}

	seconds := elapsed.Seconds()
	m.executions.Add(ctx, 1, metric.WithAttributes(attributeResult.String(result)))
	m.duration.Record(ctx, seconds)
	m.lastDuration.Record(ctx, seconds)
}
