// Package api serves the HTTP surface of the backend.
//
// Two routes, and no more: the frontend and the Ingress both depend on them.
// See k8s/README.md section 4 for the shapes below.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// RootSpanName is the name of the span the middleware makes. Section 7 of the
// contract fixes it, and the Tempo panels search for this exact text.
const RootSpanName = "POST /api/run"

// RequestTimeout bounds one whole request.
//
// It must sit above the sum of the stages it contains, or it fires first and
// hides the specific error underneath a generic one: 10 s of compile, 5 s of
// execution, 10 s to add and 5 s to announce is 30, and this leaves five over.
// The stages read it from the context and each derives its own from it.
const RequestTimeout = 35 * time.Second

// MaxSourceBytes caps the source code of one request.
//
// A playground snippet is a few hundred bytes. The cap is far above that and far
// below anything that would keep rustc busy, and it is applied before the body is
// read, so a large body costs no memory. plan.md asks for it under V.1.3: rustc
// runs the user's code at compile time, so the cheapest limits come first.
const MaxSourceBytes = 64 * 1024

// The kinds of failure the frontend styles apart. The subject requires a compile
// error and a runtime error to look different, so the kind arrives from here and
// is never guessed from the text of the message.
const (
	KindCompile = "compile"
	KindTimeout = "timeout"

	// KindRuntime covers every way a program can stop once it has started, and
	// that includes running out of memory. A guest refused more memory is refused
	// by the sandbox, and Rust answers by printing what happened and calling
	// abort() — a trap, the same mechanism as any panic. Telling the two apart
	// would mean reading the text the program printed, and the rule above is that
	// a kind is never read from a message. See k8s/README.md section 4.
	KindRuntime = "runtime"

	KindOutputLimit = "output_limit"
	KindInternal    = "internal"

	// KindRequest covers a request that never became a run: a body that is not
	// JSON, or one above MaxSourceBytes. The five kinds above all describe a run
	// that started, so none of them can carry this without lying.
	KindRequest = "request"
)

// RunRequest is the body the frontend posts to /api/run.
type RunRequest struct {
	Code string `json:"code"`
}

// Timings reports how long each stage of the pipeline took.
type Timings struct {
	CompileMS int64 `json:"compile_ms"`
	ExecuteMS int64 `json:"execute_ms"`
	UploadMS  int64 `json:"upload_ms"`
}

// RunError says what went wrong and of which kind.
type RunError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// RunResponse is the answer of /api/run. A run that fails answers with this same
// shape and with HTTP 200, because a failed compile is a result, not an incident.
//
// An empty CID with no Error is not a contradiction: the run succeeded and only
// the sharing failed.
type RunResponse struct {
	Output string `json:"output"`
	CID    string `json:"cid"`

	// Link is the address the browser opens, built by the backend from
	// IPFS_GATEWAY_URL. The page is static and cannot read that address from
	// anywhere, and holding the gateway host in the JavaScript as well would put
	// one contract value in two places.
	Link string `json:"link"`

	Timings Timings   `json:"timings"`
	Error   *RunError `json:"error"`
}

// Runner turns source code into a finished run. It returns no error, because
// every failure a run can have belongs in the body as a kind.
type Runner interface {
	Run(ctx context.Context, code string) RunResponse
}

type handler struct {
	runner Runner
	logger *slog.Logger
}

// NewHandler wires the two routes. The method is part of the pattern, so a GET on
// /api/run answers 405 without a line of code here.
func NewHandler(runner Runner, logger *slog.Logger) http.Handler {
	h := &handler{runner: runner, logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)

	// Only the run is traced. The two probes call /healthz every few seconds, and
	// a trace for each of them would bury the runs; section 7 of the contract says
	// one run is one trace of exactly four spans, not one trace among hundreds.
	//
	// The operation name is fixed rather than derived, because it is the name the
	// Tempo panels search for.
	mux.Handle("POST /api/run", otelhttp.NewHandler(http.HandlerFunc(h.run), RootSpanName))
	return mux
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) run(w http.ResponseWriter, r *http.Request) {
	// The deadline for the whole request. Nothing bounded the pair of stages
	// before: compile is capped at 10 s and execute at 5 s, the upload adds 10 s
	// and 5 s more, and a client that walks away left all of it running.
	//
	// It sits above the sum on purpose. A deadline shorter than its parts would
	// fire first and turn a compile timeout into a request that died with nothing
	// to say, which is exactly the error message we would not want to debug.
	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()

	// MaxBytesReader stops the read at the cap instead of buffering the whole
	// body first, so an oversized request costs nothing to refuse.
	r.Body = http.MaxBytesReader(w, r.Body, MaxSourceBytes)

	var request RunRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		var toolarge *http.MaxBytesError
		if errors.As(err, &toolarge) {
			h.writeError(ctx, w, KindRequest, "the source code is larger than the limit")
			return
		}
		h.writeError(ctx, w, KindRequest, "the body is not the expected JSON object")
		return
	}

	if request.Code == "" {
		h.writeError(ctx, w, KindRequest, "the field code is empty")
		return
	}

	h.writeJSON(ctx, w, http.StatusOK, h.runner.Run(ctx, request.Code))
}

// writeError answers a request that never became a run. Its status is 4xx, unlike
// a failed run, and its body carries the same shape so the frontend parses one
// thing and not two.
func (h *handler) writeError(ctx context.Context, w http.ResponseWriter,
	kind, message string) {
	h.writeJSON(ctx, w, http.StatusBadRequest, RunResponse{
		Error: &RunError{Kind: kind, Message: message},
	})
}

// writeJSON takes a context only to log with. The line has to travel under the
// span of the request, or it reaches Loki with no trace identifier and "Logs for
// this span" never shows it.
func (h *handler) writeJSON(ctx context.Context, w http.ResponseWriter,
	status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	// The status line is already sent, so a failed encode cannot become an HTTP
	// error. Logging it is all that is left, and staying silent would hide it.
	if err := json.NewEncoder(w).Encode(body); err != nil {
		h.logger.ErrorContext(ctx, "could not write the answer", "error", err)
	}
}
