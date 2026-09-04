package run

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/api"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/compile"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/execute"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/ipfs"
)

// traced runs one pipeline under a recorded root span and returns every span
// that ended, the root last.
//
// The root stands in for the span otelhttp makes around the request. The
// pipeline never creates it, so a test that did not provide one would prove
// nothing about the shape of a real trace.
func traced(t *testing.T, pipeline *Pipeline, code string) ([]sdktrace.ReadOnlySpan, api.RunResponse) {
	t.Helper()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	ctx, root := provider.Tracer("test").Start(context.Background(), api.RootSpanName)
	answer := pipeline.Run(ctx, code)
	root.End()

	return recorder.Ended(), answer
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

func attributeOf(span sdktrace.ReadOnlySpan, key attribute.Key) (string, bool) {
	for _, kv := range span.Attributes() {
		if kv.Key == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
}

func successfulPipeline() *Pipeline {
	pipeline := compiled(&fakeExecutor{result: &execute.Result{Output: "hello\n"}})
	pipeline.Uploader = &fakeUploader{result: &ipfs.Result{
		CID:  "QmDirectory",
		Link: "http://ipfs.lgtm.local/ipfs/QmDirectory",
	}}
	return pipeline
}

// Section 7 of the contract: one run is one trace of exactly four spans.
func TestARunMakesFourSpans(t *testing.T) {
	spans, _ := traced(t, successfulPipeline(), "fn main() {}")

	want := []string{compileSpanName, executeSpanName, uploadSpanName, api.RootSpanName}
	got := spanNames(spans)

	if len(got) != len(want) {
		t.Fatalf("the trace has %d spans, %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("span %d is %q, want %q", i, got[i], want[i])
		}
	}
}

// The three stages hang under the root, not under each other. A trace that nests
// them in a chain reads as one thing waiting on the next.
func TestTheThreeStagesAreChildrenOfTheRoot(t *testing.T) {
	spans, _ := traced(t, successfulPipeline(), "fn main() {}")

	root := spans[len(spans)-1]
	for _, span := range spans[:len(spans)-1] {
		if span.Parent().SpanID() != root.SpanContext().SpanID() {
			t.Errorf("%s hangs under %v, want the root", span.Name(), span.Parent().SpanID())
		}
	}
}

// code.sha256 on every span, so a person reading a trace can say which source
// ran. The source itself never travels: it has no bound on its size.
func TestEverySpanCarriesTheHashOfTheSource(t *testing.T) {
	const code = "fn main() {}"
	spans, _ := traced(t, successfulPipeline(), code)

	want := sha256Hex(code)
	for _, span := range spans {
		got, ok := attributeOf(span, attributeCodeSHA256)
		if !ok {
			t.Errorf("%s carries no code.sha256", span.Name())
			continue
		}
		if got != want {
			t.Errorf("%s carries %q, want %q", span.Name(), got, want)
		}
	}
}

func TestTheRootCarriesTheCID(t *testing.T) {
	spans, _ := traced(t, successfulPipeline(), "fn main() {}")

	root := spans[len(spans)-1]
	got, ok := attributeOf(root, attributeIPFSCID)
	if !ok {
		t.Fatal("the root carries no ipfs.cid")
	}
	if got != "QmDirectory" {
		t.Errorf("ipfs.cid is %q", got)
	}
}

// RecordError alone leaves a span green, and a failed run that reads as green is
// a trace that lies. Both calls, on the span that failed.
func TestAFailedStageIsRecordedAndRed(t *testing.T) {
	pipeline := &Pipeline{Compiler: fakeCompiler{err: &compile.FailedError{Output: "no"}}}
	spans, _ := traced(t, pipeline, "fn main() {}")

	compileSpan := spans[0]
	if compileSpan.Name() != compileSpanName {
		t.Fatalf("the first span is %s", compileSpan.Name())
	}
	if compileSpan.Status().Code != codes.Error {
		t.Errorf("the compile span is %v, want an error status", compileSpan.Status().Code)
	}
	if len(compileSpan.Events()) == 0 {
		t.Error("the compile span records no error event")
	}
}

// The whole trace turns red, not only the stage. A person looking at a list of
// traces has to see which runs failed without opening each one.
func TestAFailedRunTurnsTheRootRed(t *testing.T) {
	for name, pipeline := range map[string]*Pipeline{
		"a compile failure": {Compiler: fakeCompiler{err: &compile.FailedError{Output: "no"}}},
		"a runtime failure": compiled(&fakeExecutor{err: &execute.FailedError{Output: "boom"}}),
		"a cut output": compiled(&fakeExecutor{result: &execute.Result{
			Output: "the first ten kilobytes", Truncated: true,
		}}),
	} {
		spans, _ := traced(t, pipeline, "fn main() {}")

		root := spans[len(spans)-1]
		if root.Status().Code != codes.Error {
			t.Errorf("%s: the root is %v, want an error status", name, root.Status().Code)
		}
	}
}

// The run worked and only the sharing did not, which is what the response says
// too. A red root here would report a failure the user never had.
func TestAFailedUploadLeavesTheRootGreen(t *testing.T) {
	pipeline := compiled(&fakeExecutor{result: &execute.Result{Output: "hello\n"}})
	pipeline.Uploader = &fakeUploader{err: errors.New("the node refused")}

	spans, answer := traced(t, pipeline, "fn main() {}")

	if answer.Error != nil {
		t.Fatalf("the answer carries %+v, want none", answer.Error)
	}

	root := spans[len(spans)-1]
	if root.Status().Code == codes.Error {
		t.Error("the root is red, and the run itself succeeded")
	}

	upload := spans[len(spans)-2]
	if upload.Name() != uploadSpanName {
		t.Fatalf("the span before the root is %s", upload.Name())
	}
	if upload.Status().Code != codes.Error {
		t.Error("the upload span is not red, and the upload failed")
	}
}
