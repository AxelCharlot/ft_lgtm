package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingRunner keeps the code it was given, so a test can prove the body
// reached the pipeline unchanged.
type recordingRunner struct {
	code   string
	answer RunResponse
}

func (r *recordingRunner) Run(_ context.Context, code string) RunResponse {
	r.code = code
	return r.answer
}

func post(t *testing.T, handler http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder) RunResponse {
	t.Helper()

	var answer RunResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("the body %q is not a RunResponse: %v", recorder.Body.String(), err)
	}
	return answer
}

func TestHealthzAnswersOK(t *testing.T) {
	handler := NewHandler(&recordingRunner{}, discardLogger())

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type %q, want application/json", got)
	}
}

func TestRunPassesTheCodeAndReturnsTheAnswer(t *testing.T) {
	runner := &recordingRunner{answer: RunResponse{
		Output:  "hello\n",
		CID:     "bafyexample",
		Timings: Timings{CompileMS: 812, ExecuteMS: 4, UploadMS: 37},
	}}
	handler := NewHandler(runner, discardLogger())

	const code = "fn main() { println!(\"hello\"); }"
	recorder := post(t, handler, `{"code":`+quote(code)+`}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", recorder.Code, http.StatusOK)
	}
	if runner.code != code {
		t.Errorf("the runner got %q, want %q", runner.code, code)
	}

	answer := decode(t, recorder)
	if answer.Output != "hello\n" || answer.CID != "bafyexample" {
		t.Errorf("answer = %+v, want the output and the cid of the runner", answer)
	}
	if answer.Timings.CompileMS != 812 {
		t.Errorf("compile_ms = %d, want 812", answer.Timings.CompileMS)
	}
	if answer.Error != nil {
		t.Errorf("error = %+v, want nil", answer.Error)
	}
}

// A run that fails is still HTTP 200: a failed compile is a result, not an
// incident. See k8s/README.md section 4.
func TestAFailedRunStillAnswersTwoHundred(t *testing.T) {
	runner := &recordingRunner{answer: RunResponse{
		Error: &RunError{Kind: KindCompile, Message: "expected `;`"},
	}}
	handler := NewHandler(runner, discardLogger())

	recorder := post(t, handler, `{"code":"fn main() {}"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want %d", recorder.Code, http.StatusOK)
	}
	answer := decode(t, recorder)
	if answer.Error == nil || answer.Error.Kind != KindCompile {
		t.Errorf("error = %+v, want the compile kind", answer.Error)
	}
}

func TestRunRefusesABadRequest(t *testing.T) {
	cases := map[string]string{
		"a body that is not JSON": "this is not json",
		"an empty field code":     `{"code":""}`,
		"source above the cap":    `{"code":` + quote(strings.Repeat("a", MaxSourceBytes+1)) + `}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			runner := &recordingRunner{}
			handler := NewHandler(runner, discardLogger())

			recorder := post(t, handler, body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want %d", recorder.Code, http.StatusBadRequest)
			}
			answer := decode(t, recorder)
			if answer.Error == nil || answer.Error.Kind != KindRequest {
				t.Errorf("error = %+v, want the request kind", answer.Error)
			}
			if runner.code != "" {
				t.Errorf("the runner ran on %q, and it should not have run at all", runner.code)
			}
		})
	}
}

// The method is part of the route pattern, so this costs no code in the handler.
func TestRunRefusesTheWrongMethod(t *testing.T) {
	handler := NewHandler(&recordingRunner{}, discardLogger())

	request := httptest.NewRequest(http.MethodGet, "/api/run", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func quote(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
