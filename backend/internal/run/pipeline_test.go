package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/api"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/compile"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/execute"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/ipfs"
)

type fakeCompiler struct {
	result *compile.Result
	err    error
}

func (f fakeCompiler) Compile(context.Context, string) (*compile.Result, error) {
	return f.result, f.err
}

type fakeExecutor struct {
	module []byte
	result *execute.Result
	err    error
}

func (f *fakeExecutor) Execute(_ context.Context, module []byte) (*execute.Result, error) {
	f.module = module
	return f.result, f.err
}

// compiled is a pipeline whose compiler always succeeds, so a test can be about
// the second stage alone.
func compiled(executor Executor) *Pipeline {
	return &Pipeline{
		Compiler: fakeCompiler{result: &compile.Result{
			Module:   []byte{0x00, 0x61, 0x73, 0x6d},
			Duration: 812 * time.Millisecond,
		}},
		Executor: executor,
	}
}

func TestRunReportsACompileFailureAsACompileError(t *testing.T) {
	const message = "error[E0425]: cannot find value `x` in this scope"
	pipeline := &Pipeline{Compiler: fakeCompiler{err: &compile.FailedError{Output: message}}}

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.Error == nil {
		t.Fatal("the answer carries no error")
	}
	if answer.Error.Kind != api.KindCompile {
		t.Errorf("kind = %q, want %q", answer.Error.Kind, api.KindCompile)
	}
	if answer.Error.Message != message {
		t.Errorf("message = %q, want the text rustc printed", answer.Error.Message)
	}
}

func TestRunReportsACompileDeadlineAsATimeout(t *testing.T) {
	pipeline := &Pipeline{Compiler: fakeCompiler{err: compile.ErrTimeout}}

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.Error == nil || answer.Error.Kind != api.KindTimeout {
		t.Fatalf("error = %+v, want the timeout kind", answer.Error)
	}
}

func TestRunReportsOurOwnFailureAsInternal(t *testing.T) {
	pipeline := &Pipeline{Compiler: fakeCompiler{err: errors.New("could not create the build directory")}}

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.Error == nil || answer.Error.Kind != api.KindInternal {
		t.Fatalf("error = %+v, want the internal kind", answer.Error)
	}
}

// This is the line the subject cares about: the two failures must not look alike.
func TestRunReportsAProgramFailureAsARuntimeError(t *testing.T) {
	pipeline := compiled(&fakeExecutor{err: &execute.FailedError{
		Output:   "thread 'main' panicked at main.rs:1:13:\nboom\n",
		ExitCode: 101,
	}})

	answer := pipeline.Run(context.Background(), "fn main() { panic!() }")

	if answer.Error == nil || answer.Error.Kind != api.KindRuntime {
		t.Fatalf("error = %+v, want the runtime kind", answer.Error)
	}
	if answer.Output == "" {
		t.Error("the output is empty, and the panic message belongs to the user")
	}
}

func TestRunReportsAnExecutionDeadlineAsATimeout(t *testing.T) {
	pipeline := compiled(&fakeExecutor{err: execute.ErrTimeout})

	answer := pipeline.Run(context.Background(), "fn main() { loop {} }")

	if answer.Error == nil || answer.Error.Kind != api.KindTimeout {
		t.Fatalf("error = %+v, want the timeout kind", answer.Error)
	}
}

// An empty cid with no error is what the contract calls a run that worked and was
// not shared. A Pipeline with no Uploader is exactly that state.
func TestRunSucceedsWithNoUploader(t *testing.T) {
	executor := &fakeExecutor{result: &execute.Result{
		Output:   "hello\n",
		Duration: 4 * time.Millisecond,
	}}
	pipeline := compiled(executor)

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.Error != nil {
		t.Fatalf("error = %+v, want none", answer.Error)
	}
	if answer.Output != "hello\n" {
		t.Errorf("output = %q, want %q", answer.Output, "hello\n")
	}
	if answer.CID != "" {
		t.Errorf("cid = %q, want it empty with no uploader", answer.CID)
	}
	if answer.Timings.CompileMS != 812 || answer.Timings.ExecuteMS != 4 {
		t.Errorf("timings = %+v, want 812 and 4", answer.Timings)
	}
	if string(executor.module) != string([]byte{0x00, 0x61, 0x73, 0x6d}) {
		t.Errorf("the executor got %v, want the module the compiler produced", executor.module)
	}
}

// A run that was cut is still a run that worked. The user is told, and the output
// that did arrive is kept.
func TestRunSaysWhenTheOutputWasCut(t *testing.T) {
	pipeline := compiled(&fakeExecutor{result: &execute.Result{
		Output:    "the first ten kilobytes",
		Truncated: true,
	}})

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.Error == nil || answer.Error.Kind != api.KindOutputLimit {
		t.Fatalf("error = %+v, want the output_limit kind", answer.Error)
	}
	if answer.Output != "the first ten kilobytes" {
		t.Errorf("output = %q, want what did arrive", answer.Output)
	}
}

// fakeUploader stands in for the node. It records what it was handed, because
// what travels to IPFS is the source the user sent and the output it produced.
type fakeUploader struct {
	result *ipfs.Result
	err    error

	source string
	output string
	called bool
}

func (f *fakeUploader) Upload(_ context.Context, source, output string) (*ipfs.Result, error) {
	f.called = true
	f.source = source
	f.output = output
	return f.result, f.err
}

func TestRunSharesASuccessfulRun(t *testing.T) {
	uploader := &fakeUploader{result: &ipfs.Result{
		CID:      "QmDirectory",
		Link:     "http://ipfs.lgtm.local/ipfs/QmDirectory",
		Duration: 37 * time.Millisecond,
	}}
	pipeline := compiled(&fakeExecutor{result: &execute.Result{Output: "hello\n"}})
	pipeline.Uploader = uploader

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.CID != "QmDirectory" {
		t.Errorf("cid = %q", answer.CID)
	}
	if answer.Link != "http://ipfs.lgtm.local/ipfs/QmDirectory" {
		t.Errorf("link = %q", answer.Link)
	}
	if answer.Timings.UploadMS != 37 {
		t.Errorf("upload_ms = %d, want 37", answer.Timings.UploadMS)
	}
	if uploader.source != "fn main() {}" || uploader.output != "hello\n" {
		t.Errorf("the node was handed %q and %q", uploader.source, uploader.output)
	}
}

// The contract is explicit: a failed upload leaves the cid empty and does not set
// an error. The run worked, and only the sharing did not.
func TestRunKeepsSucceedingWhenTheUploadFails(t *testing.T) {
	pipeline := compiled(&fakeExecutor{result: &execute.Result{Output: "hello\n"}})
	pipeline.Uploader = &fakeUploader{err: errors.New("the node refused")}

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.Error != nil {
		t.Fatalf("error = %+v, want none: the run itself worked", answer.Error)
	}
	if answer.CID != "" || answer.Link != "" {
		t.Errorf("cid = %q and link = %q, want both empty", answer.CID, answer.Link)
	}
	if answer.Output != "hello\n" {
		t.Errorf("output = %q, want the program's own output", answer.Output)
	}
}

// A failed announce still returns a result: the directory is added and pinned, so
// our own gateway serves the link already.
func TestRunKeepsTheLinkWhenOnlyTheAnnounceFailed(t *testing.T) {
	pipeline := compiled(&fakeExecutor{result: &execute.Result{Output: "hello\n"}})
	pipeline.Uploader = &fakeUploader{
		result: &ipfs.Result{CID: "QmDirectory", Link: "http://ipfs.lgtm.local/ipfs/QmDirectory"},
		err:    errors.New("the announce timed out"),
	}

	answer := pipeline.Run(context.Background(), "fn main() {}")

	if answer.Error != nil {
		t.Fatalf("error = %+v, want none", answer.Error)
	}
	if answer.CID != "QmDirectory" || answer.Link == "" {
		t.Errorf("the link was thrown away: cid = %q, link = %q", answer.CID, answer.Link)
	}
}

// A source that did not compile has no output to put beside it, and a run that
// was cut would put a file on IPFS that does not say it is short.
func TestRunSharesNothingWhenTheRunFailed(t *testing.T) {
	for name, pipeline := range map[string]*Pipeline{
		"a compile failure": {Compiler: fakeCompiler{err: &compile.FailedError{Output: "no"}}},
		"a cut output": compiled(&fakeExecutor{result: &execute.Result{
			Output: "the first ten kilobytes", Truncated: true,
		}}),
	} {
		uploader := &fakeUploader{}
		pipeline.Uploader = uploader

		if answer := pipeline.Run(context.Background(), "fn main() {}"); answer.CID != "" {
			t.Errorf("%s: cid = %q, want it empty", name, answer.CID)
		}
		if uploader.called {
			t.Errorf("%s: the node was called anyway", name)
		}
	}
}
