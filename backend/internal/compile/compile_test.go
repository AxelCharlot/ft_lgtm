package compile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const helloWorld = `fn main() { println!("hello"); }`

// wasmMagic starts every WebAssembly module.
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// requireRustc skips a test that needs the real toolchain. The unit tests below
// run anywhere; these run where the backend image is built.
func requireRustc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc is not on the PATH")
	}
}

// scriptCompiler writes a shell script and returns a Compiler that calls it.
// It stands in for rustc where the test is about this package and not about Rust.
func scriptCompiler(t *testing.T, body string) *Compiler {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-rustc")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("could not write the script: %v", err)
	}
	return &Compiler{Path: path}
}

func TestCompileProducesAModule(t *testing.T) {
	requireRustc(t)

	result, err := (&Compiler{}).Compile(context.Background(), helloWorld)
	if err != nil {
		// The output of the compiler is the whole story when this fails, and
		// leaving it out costs a debugging round trip.
		var failed *FailedError
		if errors.As(err, &failed) {
			t.Fatalf("Compile refused valid source, and rustc said:\n%s", failed.Output)
		}
		t.Fatalf("Compile returned %v, want a module", err)
	}
	if len(result.Module) == 0 {
		t.Fatal("the module is empty")
	}
	if got := result.Module[:4]; string(got) != string(wasmMagic) {
		t.Errorf("the module starts with %v, want the WebAssembly magic %v", got, wasmMagic)
	}
	if result.Duration <= 0 {
		t.Errorf("duration = %v, want more than zero", result.Duration)
	}
}

// The toolchain is configured through the environment, so an allowlist that is
// too tight looks exactly like a source that does not compile.
func TestCompilePassesTheToolchainVariables(t *testing.T) {
	t.Setenv("RUSTUP_HOME", "/usr/local/rustup")

	variables := environment("/build")

	if variables[0] != "TMPDIR=/build" {
		t.Errorf("the first variable is %q, want TMPDIR", variables[0])
	}
	found := false
	for _, variable := range variables {
		if variable == "RUSTUP_HOME=/usr/local/rustup" {
			found = true
		}
		if strings.HasPrefix(variable, "OTEL_") || strings.HasPrefix(variable, "IPFS_") {
			t.Errorf("%q reached the compiler, and nothing of ours should", variable)
		}
	}
	if !found {
		t.Error("RUSTUP_HOME did not reach the compiler, and rustup needs it")
	}
}

func TestCompileReturnsTheMessageOfTheCompiler(t *testing.T) {
	requireRustc(t)

	_, err := (&Compiler{}).Compile(context.Background(), "fn main() { let x: i32 = }")

	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Compile returned %v, want a *FailedError", err)
	}
	if !strings.Contains(failed.Output, "error") {
		t.Errorf("the output is %q, and it does not carry a message from rustc", failed.Output)
	}
}

// A source that does not compile is a compile failure and never a timeout or an
// internal one. The kind the frontend styles comes from this distinction.
func TestCompileTellsAFailureFromAnInternalProblem(t *testing.T) {
	compiler := &Compiler{Path: filepath.Join(t.TempDir(), "does-not-exist")}

	_, err := compiler.Compile(context.Background(), helloWorld)

	var failed *FailedError
	if errors.As(err, &failed) {
		t.Fatalf("a missing compiler was reported as a failed compilation: %v", err)
	}
	if err == nil {
		t.Fatal("Compile succeeded with no compiler, want an error")
	}
}

func TestCompileReportsATimeout(t *testing.T) {
	compiler := scriptCompiler(t, "sleep 30")
	compiler.Timeout = 100 * time.Millisecond

	started := time.Now()
	_, err := compiler.Compile(context.Background(), helloWorld)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Compile returned %v, want ErrTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Compile took %v, so the deadline did not stop it", elapsed)
	}
}

// The pod has one writable directory, and a run that leaves its files behind
// fills it for every later request.
func TestCompileRemovesItsDirectory(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TMPDIR", parent)

	compiler := scriptCompiler(t, "exit 1")
	if _, err := compiler.Compile(context.Background(), helloWorld); err == nil {
		t.Fatal("Compile succeeded, want the failure this test is about")
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("could not read %s: %v", parent, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "lgtm-compile-") {
			t.Errorf("%s was left behind", entry.Name())
		}
	}
}

// A compiler that answers 0 and writes nothing is broken, not successful.
func TestCompileRefusesAMissingModule(t *testing.T) {
	compiler := scriptCompiler(t, "exit 0")

	_, err := compiler.Compile(context.Background(), helloWorld)

	if err == nil {
		t.Fatal("Compile succeeded with no module, want an error")
	}
	var failed *FailedError
	if errors.As(err, &failed) {
		t.Errorf("returned %v, want an internal error and not a compile failure", err)
	}
}
