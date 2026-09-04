package run

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/api"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/compile"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/execute"
)

// captured keeps every record the bridge emits, in order. It stands where the
// OTLP exporter stands in the pod, so the test reads exactly what Loki would.
type captured struct {
	mutex   sync.Mutex
	records []sdklog.Record
}

func (c *captured) OnEmit(_ context.Context, record *sdklog.Record) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.records = append(c.records, *record)
	return nil
}

// Enabled decides whether the bridge bothers to build a record at all. Always
// here: a test that dropped a line before capturing it would prove the opposite
// of what it claims.
func (c *captured) Enabled(context.Context, sdklog.EnabledParameters) bool { return true }

func (c *captured) Shutdown(context.Context) error   { return nil }
func (c *captured) ForceFlush(context.Context) error { return nil }

func (c *captured) all() []sdklog.Record {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return append([]sdklog.Record(nil), c.records...)
}

// logged runs one pipeline under a sampled root span and returns every line it
// wrote, with the trace identifier of that run.
func logged(t *testing.T, pipeline *Pipeline, code string) ([]sdklog.Record, trace.TraceID) {
	t.Helper()

	records := &captured{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(records))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("the logger provider did not stop: %v", err)
		}
	})
	pipeline.Logger = otelslog.NewLogger("test", otelslog.WithLoggerProvider(provider))

	tracers := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tracers)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	ctx, root := tracers.Tracer("test").Start(context.Background(), api.RootSpanName)
	pipeline.Run(ctx, code)
	identifier := root.SpanContext().TraceID()
	root.End()

	return records.all(), identifier
}

func messagesOf(records []sdklog.Record) []string {
	messages := make([]string, 0, len(records))
	for _, record := range records {
		messages = append(messages, record.Body().AsString())
	}
	return messages
}

// The whole of issue #18. A line that reaches Loki without the trace identifier
// is invisible to "Logs for this span", and nothing anywhere reports it: the
// button simply returns an empty panel.
func TestEveryLineOfARunCarriesTheTraceIdentifier(t *testing.T) {
	records, identifier := logged(t, successfulPipeline(), "fn main() {}")

	if len(records) == 0 {
		t.Fatal("the run wrote no line at all")
	}
	for _, record := range records {
		if record.TraceID() != identifier {
			t.Errorf("%q carries the trace %s, want %s of this run",
				record.Body().AsString(), record.TraceID(), identifier)
		}
		if !record.SpanID().IsValid() {
			t.Errorf("%q carries no span identifier", record.Body().AsString())
		}
	}
}

// The issue asks for the start and the end of each step. A stage that only logs
// when it ends cannot be told apart from a stage that hung.
func TestARunLogsTheStartAndTheEndOfEveryStage(t *testing.T) {
	records, _ := logged(t, successfulPipeline(), "fn main() {}")
	got := messagesOf(records)

	for _, want := range []string{
		"the compile stage started",
		"the compile stage finished",
		"the execute stage started",
		"the execute stage finished",
		"the upload stage started",
		"the upload stage finished",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("no line says %q, the run wrote %v", want, got)
		}
	}
}

// Every error, says the issue. A failed stage that logs nothing leaves the trace
// red in Tempo and the log panel beside it empty, which reads as a bug in Grafana
// rather than a failure of the run.
func TestAFailedStageIsLoggedAsAnError(t *testing.T) {
	cases := map[string]struct {
		pipeline *Pipeline
		want     string
	}{
		"a source that does not compile": {
			&Pipeline{Compiler: fakeCompiler{err: &compile.FailedError{Output: "no"}}},
			"the compile stage failed",
		},
		"a program that traps": {
			compiled(&fakeExecutor{err: &execute.FailedError{Output: "boom"}}),
			"the execute stage failed",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			records, identifier := logged(t, c.pipeline, "fn main() {}")

			var found bool
			for _, record := range records {
				if record.Body().AsString() != c.want {
					continue
				}
				found = true
				if record.Severity() != log.SeverityError {
					t.Errorf("%q has the severity %v, want %v",
						c.want, record.Severity(), log.SeverityError)
				}
				if record.TraceID() != identifier {
					t.Errorf("%q carries the trace %s, want %s", c.want,
						record.TraceID(), identifier)
				}
			}
			if !found {
				t.Errorf("no line says %q, the run wrote %v", c.want, messagesOf(records))
			}
		})
	}
}

// The upload is the one failure the run swallows: the contract says the run
// succeeded and only the sharing did not. That makes the log line the only place
// it is ever reported.
func TestAFailedUploadIsTheOnlyReportOfItself(t *testing.T) {
	pipeline := compiled(&fakeExecutor{result: &execute.Result{Output: "hello\n"}})
	pipeline.Uploader = &fakeUploader{err: errors.New("the node refused")}

	records, _ := logged(t, pipeline, "fn main() {}")

	if !slices.Contains(messagesOf(records), "the run was not shared") {
		t.Errorf("nothing says the sharing failed, the run wrote %v", messagesOf(records))
	}
}

// Every test of the stages builds a Pipeline with no Logger, and a nil one has to
// stay a pipeline that runs.
func TestAPipelineWithNoLoggerStillRuns(t *testing.T) {
	pipeline := successfulPipeline()
	pipeline.Logger = nil

	answer := pipeline.Run(context.Background(), "fn main() {}")
	if answer.Error != nil {
		t.Fatalf("the run answered %v, want no error", answer.Error)
	}
}
