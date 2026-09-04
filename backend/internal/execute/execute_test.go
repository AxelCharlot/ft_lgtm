package execute

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/compile"
)

// moduleFrom compiles Rust into a module. The alternative is a wasm file in the
// repository, and a binary nobody can read is worse than a test that skips.
func moduleFrom(t *testing.T, source string) []byte {
	t.Helper()
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc is not on the PATH")
	}

	result, err := (&compile.Compiler{}).Compile(context.Background(), source)
	if err != nil {
		t.Fatalf("the source for this test does not compile: %v", err)
	}
	return result.Module
}

func TestExecuteReturnsWhatTheProgramPrinted(t *testing.T) {
	module := moduleFrom(t, `fn main() { println!("hello from wasm"); }`)

	result, err := (&Executor{}).Execute(context.Background(), module)
	if err != nil {
		t.Fatalf("Execute returned %v, want the output", err)
	}
	if result.Output != "hello from wasm\n" {
		t.Errorf("Output = %q, want %q", result.Output, "hello from wasm\n")
	}
	if result.Truncated {
		t.Error("Truncated is true for one short line")
	}
}

// Standard error is output too. A panic writes there, and to a person reading the
// playground it is simply what the program said.
func TestExecuteMergesStandardErrorIntoTheOutput(t *testing.T) {
	module := moduleFrom(t, `fn main() { eprintln!("to stderr"); println!("to stdout"); }`)

	result, err := (&Executor{}).Execute(context.Background(), module)
	if err != nil {
		t.Fatalf("Execute returned %v", err)
	}
	for _, want := range []string{"to stderr", "to stdout"} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("Output = %q, and it is missing %q", result.Output, want)
		}
	}
}

// A panic is a runtime failure, and it must never be reported as anything else.
func TestExecuteReportsAPanicAsAFailure(t *testing.T) {
	module := moduleFrom(t, `fn main() { panic!("boom"); }`)

	_, err := (&Executor{}).Execute(context.Background(), module)

	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Execute returned %v, want a *FailedError", err)
	}
	// A panic is a trap and not an exit, so there is no exit code to read. What
	// there is, is the message the panic hook printed before aborting.
	if failed.Reason == "" {
		t.Error("Reason is empty, and the program trapped")
	}
	if !strings.Contains(failed.Output, "boom") {
		t.Errorf("Output = %q, and it does not carry the panic message", failed.Output)
	}
	if strings.Contains(failed.Error(), "\n") {
		t.Errorf("Error() = %q, and a guest stack trace does not belong in a panel", failed.Error())
	}
}

// A non-zero exit with no panic is still the program's own failure.
func TestExecuteReportsANonZeroExit(t *testing.T) {
	module := moduleFrom(t, `fn main() { std::process::exit(3); }`)

	_, err := (&Executor{}).Execute(context.Background(), module)

	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Execute returned %v, want a *FailedError", err)
	}
	if failed.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", failed.ExitCode)
	}
}

// The deadline has to reach a module that never yields, which is what
// WithCloseOnContextDone buys.
func TestExecuteStopsAModuleThatNeverEnds(t *testing.T) {
	module := moduleFrom(t, `fn main() { loop {} }`)
	executor := &Executor{Timeout: 200 * time.Millisecond}

	started := time.Now()
	_, err := executor.Execute(context.Background(), module)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Execute returned %v, want ErrTimeout", err)
	}
	if elapsed > 10*time.Second {
		t.Errorf("Execute took %v, so the deadline did not reach the module", elapsed)
	}
}

func TestExecuteCutsOutputAtTheLimit(t *testing.T) {
	module := moduleFrom(t, `fn main() { for _ in 0..100000 { println!("a line of output"); } }`)
	executor := &Executor{MaxOutputBytes: 1024}

	result, err := executor.Execute(context.Background(), module)
	if err != nil {
		t.Fatalf("Execute returned %v", err)
	}
	if !result.Truncated {
		t.Error("Truncated is false after a hundred thousand lines")
	}
	if !strings.Contains(result.Output, "more bytes were dropped") {
		t.Errorf("Output = %q, and it does not say that it was cut", result.Output[:200])
	}
}

// An out of bounds access traps like a panic does, and it is still the program's
// doing.
func TestExecuteReportsATrapAsAFailure(t *testing.T) {
	module := moduleFrom(t, `fn main() {
    let numbers = [1, 2, 3];
    let index = std::env::args().count() + 10;
    println!("{}", numbers[index]);
}`)

	_, err := (&Executor{}).Execute(context.Background(), module)

	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Execute returned %v, want a *FailedError", err)
	}
}

// A module that will not load is our problem, never the user's runtime failure.
func TestExecuteTellsABadModuleFromAFailedProgram(t *testing.T) {
	_, err := (&Executor{}).Execute(context.Background(), []byte("this is not WebAssembly"))

	if err == nil {
		t.Fatal("Execute accepted bytes that are not a module")
	}
	var failed *FailedError
	if errors.As(err, &failed) {
		t.Errorf("returned %v, want an internal error and not a program failure", err)
	}
}

func TestReasonOfKeepsOnlyTheTrap(t *testing.T) {
	cases := map[string]struct{ given, want string }{
		"a trap from wazero": {
			given: "module[] function[_start] failed: wasm error: unreachable\nwasm stack trace:\n\tout.wasm.abort()",
			want:  "unreachable",
		},
		"an error with no trap in it": {
			given: "something else went wrong\nwith a second line",
			want:  "something else went wrong",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := reasonOf(errors.New(c.given)); got != c.want {
				t.Errorf("reasonOf() = %q, want %q", got, c.want)
			}
		})
	}
}

// The five limits of plan.md section V.1.3, one test each. Four of them are here;
// the fifth, the deadline, is TestExecuteStopsAModuleThatNeverEnds above.

// The subject fixes these three numbers. A change here is a change to what the
// project promises, so it is asserted rather than assumed.
func TestTheDefaultsAreTheOnesTheSubjectAsksFor(t *testing.T) {
	if DefaultTimeout != 5*time.Second {
		t.Errorf("DefaultTimeout = %v, want 5s", DefaultTimeout)
	}
	if DefaultMaxOutputBytes != 10*1024 {
		t.Errorf("DefaultMaxOutputBytes = %d, want 10240", DefaultMaxOutputBytes)
	}
	if DefaultMemoryLimitPages != 160 {
		t.Errorf("DefaultMemoryLimitPages = %d, want 160 pages, which is 10 MiB",
			DefaultMemoryLimitPages)
	}
}

// black_box is not decoration. Without it the optimiser sees that the length of
// the vector is a constant, removes the allocation, and the program prints
// 100000000 without ever asking for a byte. The limit then looks broken when it
// is the test that never ran.
func TestExecuteRefusesMoreMemoryThanTheLimit(t *testing.T) {
	module := moduleFrom(t, `use std::hint::black_box;
fn main() {
    let size = black_box(100_000_000);
    let data = vec![0u8; size];
    println!("{}", black_box(&data).len());
}`)

	_, err := (&Executor{}).Execute(context.Background(), module)

	var failed *FailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Execute returned %v, want the program to be stopped", err)
	}
	if !strings.Contains(failed.Output, "memory allocation") {
		t.Errorf("Output = %q, and it does not say the allocation failed", failed.Output)
	}
}

// Nothing calls WithFSConfig, so the guest has no file system at all. The file
// does not fail to open for want of a permission: it does not exist.
func TestExecuteGivesNoFileSystem(t *testing.T) {
	module := moduleFrom(t, `use std::fs::File;
fn main() {
    match File::open("/etc/passwd") {
        Ok(_) => println!("OPENED"),
        Err(error) => println!("refused: {}", error),
    }
}`)

	result, err := (&Executor{}).Execute(context.Background(), module)
	if err != nil {
		t.Fatalf("Execute returned %v", err)
	}
	if strings.Contains(result.Output, "OPENED") {
		t.Fatalf("the guest read a file on this machine: %q", result.Output)
	}
	if !strings.Contains(result.Output, "refused") {
		t.Errorf("Output = %q, want the open to be refused", result.Output)
	}
}

// No socket is granted, so the guest cannot reach the network at all.
func TestExecuteGivesNoNetwork(t *testing.T) {
	module := moduleFrom(t, `use std::net::TcpStream;
fn main() {
    match TcpStream::connect("1.1.1.1:80") {
        Ok(_) => println!("CONNECTED"),
        Err(error) => println!("refused: {}", error),
    }
}`)

	result, err := (&Executor{}).Execute(context.Background(), module)
	if err != nil {
		t.Fatalf("Execute returned %v", err)
	}
	if strings.Contains(result.Output, "CONNECTED") {
		t.Fatalf("the guest opened a connection: %q", result.Output)
	}
	if !strings.Contains(result.Output, "refused") {
		t.Errorf("Output = %q, want the connection to be refused", result.Output)
	}
}

// A million lines, cut at the number the subject fixes.
func TestExecuteCutsOutputAtTheDefaultLimit(t *testing.T) {
	module := moduleFrom(t, `fn main() { for _ in 0..1_000_000 { println!("a line of output"); } }`)

	result, err := (&Executor{}).Execute(context.Background(), module)
	if err != nil {
		t.Fatalf("Execute returned %v", err)
	}
	if !result.Truncated {
		t.Fatal("Truncated is false after a million lines")
	}
	if len(result.Output) > DefaultMaxOutputBytes+200 {
		t.Errorf("the output is %d bytes, and the limit is %d plus a short note",
			len(result.Output), DefaultMaxOutputBytes)
	}
}
