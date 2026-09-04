// Package run joins the stages of one playground run and reports the result in
// the shape the contract fixes. See k8s/README.md section 4.
//
// It is also where the trace of a run is built: one span per stage, under the
// root the HTTP middleware made. See section 7.
package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/api"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/compile"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/execute"
	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/ipfs"
)

// tracerName names this instrumentation inside every span it makes.
const tracerName = "gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/run"

// The three span names of section 7. The fourth, the root, is made by the
// middleware and named in the api package.
const (
	compileSpanName = "compile"
	executeSpanName = "execute"
	uploadSpanName  = "ipfs.upload"
)

// The two attributes of section 7. The Tempo panels read these exact keys.
const (
	attributeCodeSHA256 = attribute.Key("code.sha256")
	attributeIPFSCID    = attribute.Key("ipfs.cid")
)

// errOutputLimit is the one failure no stage returns: the program ran to its end
// and the sandbox stopped listening partway. It is a failure all the same, so
// the span carries it and the response carries its message.
var errOutputLimit = errors.New(
	"the program printed more than the limit, and the rest was dropped")

// Compiler turns source into a module.
type Compiler interface {
	Compile(ctx context.Context, source string) (*compile.Result, error)
}

// Executor runs a module and returns what it printed.
type Executor interface {
	Execute(ctx context.Context, module []byte) (*execute.Result, error)
}

// Uploader shares one finished run and says where it can be read.
type Uploader interface {
	Upload(ctx context.Context, source, output string) (*ipfs.Result, error)
}

// Pipeline compiles the source, runs it, then shares it. The stages are
// interfaces so that this file can be tested with no toolchain and no node at
// all.
type Pipeline struct {
	Compiler Compiler
	Executor Executor

	// Uploader may be nil, and then nothing is shared. That is the state the
	// contract already describes: an empty cid with no error.
	Uploader Uploader

	// Logger may be nil. It carries the one failure this file swallows.
	Logger *slog.Logger

	// Metrics may be nil, and then a run is traced and not measured. The tests of
	// the stages need no meter provider to say whether a stage works.
	Metrics *Metrics
}

// Run answers with a RunResponse whatever happens, because every failure a run
// can have belongs in the body as a kind. It is also where one run becomes three
// numbers, on the way out.
func (p *Pipeline) Run(ctx context.Context, code string) api.RunResponse {
	started := time.Now()
	response := p.runStages(ctx, code)

	if p.Metrics != nil {
		// ctx still carries the root span here — the middleware ends it, not this
		// function — and the histogram has to be recorded under it. That is what
		// makes the SDK attach the trace identifier, and that identifier is the
		// whole drilldown of gate B.
		p.Metrics.Record(ctx, time.Since(started), response.Error == nil)
	}
	return response
}

// runStages is the run itself: compile, execute, share, and the answer each of
// them can end in.
func (p *Pipeline) runStages(ctx context.Context, code string) api.RunResponse {
	// The hash travels on every span, and the source never does. A trace can then
	// say which code ran, and two runs of the same source are visibly the same
	// source, without a span ever carrying a value with no bound on its size.
	digest := sha256Hex(code)

	// The root span belongs to the middleware: this function neither starts it
	// nor ends it, it only writes on it.
	root := trace.SpanFromContext(ctx)
	root.SetAttributes(attributeCodeSHA256.String(digest))

	compiled, err := p.runCompile(ctx, code, digest)
	if err != nil {
		markRootFailed(root, err)
		return api.RunResponse{Error: compileFailure(err)}
	}

	timings := api.Timings{CompileMS: compiled.Duration.Milliseconds()}

	executed, err := p.runExecute(ctx, compiled.Module, digest)
	if err != nil {
		markRootFailed(root, err)
		output, runError := executeFailure(err)
		return api.RunResponse{Output: output, Timings: timings, Error: runError}
	}
	timings.ExecuteMS = executed.Duration.Milliseconds()

	// The program ran to the end, and the sandbox stopped listening partway. That
	// is not a failure of the run, but the user has to be told: output that stops
	// mid-sentence with nothing said about it reads like a program that stopped
	// mid-sentence.
	if executed.Truncated {
		markRootFailed(root, errOutputLimit)
		return api.RunResponse{
			Output:  executed.Output,
			Timings: timings,
			Error: &api.RunError{
				Kind:    api.KindOutputLimit,
				Message: errOutputLimit.Error(),
			},
		}
	}

	// Only a run that reached its end is shared. One rule and no edge cases: a
	// source that did not compile has no output to put beside it, and a run that
	// trapped or was cut would put a file on IPFS that does not say so.
	response := api.RunResponse{Output: executed.Output, Timings: timings}
	p.share(ctx, code, digest, root, &response)
	return response
}

func (p *Pipeline) runCompile(ctx context.Context, code, digest string) (*compile.Result, error) {
	ctx, span := startSpan(ctx, compileSpanName, digest)
	defer span.End()

	p.logInfo(ctx, "the compile stage started")

	result, err := p.Compiler.Compile(ctx, code)
	if err != nil {
		markFailed(span, err)
		p.logError(ctx, "the compile stage failed", "error", err)
		return nil, err
	}

	p.logInfo(ctx, "the compile stage finished",
		"duration_ms", result.Duration.Milliseconds())
	return result, nil
}

func (p *Pipeline) runExecute(ctx context.Context, module []byte,
	digest string) (*execute.Result, error) {
	ctx, span := startSpan(ctx, executeSpanName, digest)
	defer span.End()

	p.logInfo(ctx, "the execute stage started")

	result, err := p.Executor.Execute(ctx, module)
	if err != nil {
		markFailed(span, err)
		p.logError(ctx, "the execute stage failed", "error", err)
		return nil, err
	}

	// The span says it as well as the response does, and the two must agree.
	if result.Truncated {
		markFailed(span, errOutputLimit)
		p.logError(ctx, "the execute stage failed", "error", errOutputLimit)
	}

	p.logInfo(ctx, "the execute stage finished",
		"duration_ms", result.Duration.Milliseconds())
	return result, nil
}

// share adds the run to IPFS and fills in the cid, the link and the timing.
//
// A failure here never becomes an error of the run. The contract is explicit:
// the run succeeded and only the sharing did not, so the cid stays empty and the
// error stays nil. It is logged, because a link that silently stops appearing is
// the kind of thing nobody notices for a week.
func (p *Pipeline) share(ctx context.Context, source, digest string,
	root trace.Span, response *api.RunResponse) {
	if p.Uploader == nil {
		return
	}

	ctx, span := startSpan(ctx, uploadSpanName, digest)
	defer span.End()

	p.logInfo(ctx, "the upload stage started")

	shared, err := p.Uploader.Upload(ctx, source, response.Output)

	// A failed announce still returns a result: the directory is added and
	// pinned, so the link already works on our own gateway.
	if shared != nil {
		response.CID = shared.CID
		response.Link = shared.Link
		response.Timings.UploadMS = shared.Duration.Milliseconds()

		// On the root as well, because a person opening the trace of a run looks
		// for the CID at the top and not three spans down.
		root.SetAttributes(attributeIPFSCID.String(shared.CID))
		span.SetAttributes(attributeIPFSCID.String(shared.CID))

		p.logInfo(ctx, "the upload stage finished",
			"ipfs.cid", shared.CID,
			"duration_ms", shared.Duration.Milliseconds())
	}

	if err != nil {
		// The upload span turns red and the root stays green, which is the shape
		// of the answer: the run worked, the sharing did not.
		markFailed(span, err)
		p.logError(ctx, "the run was not shared", "error", err)
	}
}

// logInfo and logError write one line under the span that ctx carries, which is
// what puts the trace identifier on it and what makes "Logs for this span" in
// Grafana return this run and no other. A line written with no context reaches
// Loki with no identifier and is invisible to that button.
//
// Both do nothing when no logger was given: the tests of the stages build a
// Pipeline without one.
func (p *Pipeline) logInfo(ctx context.Context, message string, args ...any) {
	if p.Logger != nil {
		p.Logger.InfoContext(ctx, message, args...)
	}
}

func (p *Pipeline) logError(ctx context.Context, message string, args ...any) {
	if p.Logger != nil {
		p.Logger.ErrorContext(ctx, message, args...)
	}
}

// startSpan opens one stage of the run under the span already in the context.
func startSpan(ctx context.Context, name, digest string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name,
		trace.WithAttributes(attributeCodeSHA256.String(digest)))
}

// markFailed records the error on the span where it happened.
//
// Both calls, always. RecordError alone attaches the message and leaves the span
// green in Tempo, and a failed run that reads as green is worse than no trace at
// all: it is a trace that lies.
func markFailed(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// markRootFailed colours the whole trace. Only the status is set here — the
// error event belongs to the span that produced it, and recording it on both
// would show one failure as two.
func markRootFailed(root trace.Span, err error) {
	root.SetStatus(codes.Error, err.Error())
}

func sha256Hex(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// compileFailure turns a failure of the compiler into the kind the frontend
// styles.
//
// The kind comes from the type of the error and never from the text of the
// message. A compile error and a runtime error must look different to the user,
// which the subject requires, and reading the message to decide would break the
// day rustc rewords something.
func compileFailure(err error) *api.RunError {
	var failed *compile.FailedError
	if errors.As(err, &failed) {
		return &api.RunError{Kind: api.KindCompile, Message: failed.Output}
	}

	if errors.Is(err, compile.ErrTimeout) {
		return &api.RunError{
			Kind:    api.KindTimeout,
			Message: "the compiler ran past its deadline",
		}
	}

	return &api.RunError{Kind: api.KindInternal, Message: err.Error()}
}

// executeFailure returns what the program printed and why it stopped. The output
// travels even on failure: a panic message is output, and it is the first thing
// the user looks for.
func executeFailure(err error) (string, *api.RunError) {
	var failed *execute.FailedError
	if errors.As(err, &failed) {
		return failed.Output, &api.RunError{
			Kind:    api.KindRuntime,
			Message: failed.Error(),
		}
	}

	if errors.Is(err, execute.ErrTimeout) {
		return "", &api.RunError{
			Kind:    api.KindTimeout,
			Message: "the program ran past its deadline",
		}
	}

	return "", &api.RunError{Kind: api.KindInternal, Message: err.Error()}
}
