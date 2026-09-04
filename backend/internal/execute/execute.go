// Package execute runs a WebAssembly module in a WASI sandbox.
//
// wazero is pure Go: no CGO, no external binary, nothing to install in the image.
// The sandbox grants nothing by default — no file system and no socket — so the
// limits that matter are the ones this package sets, and the ones it never opens.
// Issue #10 fixes the values the subject asks for and proves each of them.
package execute

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/limited"
)

const (
	// The three numbers below are the subject's, and they are not ours to soften.
	// plan.md section V.1.3 maps each one onto the wazero option that enforces it.

	// DefaultTimeout bounds one run.
	DefaultTimeout = 5 * time.Second

	// DefaultMaxOutputBytes bounds what one run may print.
	DefaultMaxOutputBytes = 10 * 1024

	// DefaultMemoryLimitPages bounds the linear memory of the guest. A page is
	// 64 KiB, so 160 pages is 10 MiB.
	DefaultMemoryLimitPages = 160

	// programName is argv[0] inside the sandbox. A Rust panic prints it, so it
	// should read like a program and not like a path from this machine.
	programName = "main"
)

// ErrTimeout says the program ran past its deadline.
var ErrTimeout = errors.New("the program ran past its deadline")

// FailedError says the program itself failed. It carries what the program had
// printed by then, because a panic message is output and the user wants to read
// it.
//
// A program fails in one of two ways, and Rust uses both. std::process::exit
// leaves through proc_exit and sets ExitCode. A panic does not: it reaches
// abort(), which is the wasm unreachable instruction, and that is a trap with no
// exit code at all. Reason then says what wazero reported.
type FailedError struct {
	Output    string
	ExitCode  uint32
	Reason    string
	Truncated bool
}

func (e *FailedError) Error() string {
	if e.Reason != "" {
		return "the program stopped: " + e.Reason
	}
	return fmt.Sprintf("the program exited with code %d", e.ExitCode)
}

// Result is what a program printed and how long it took.
type Result struct {
	Output    string
	Duration  time.Duration
	Truncated bool
}

// Executor runs modules. The zero value works and uses the defaults above.
type Executor struct {
	// Timeout bounds one run. Zero means DefaultTimeout.
	Timeout time.Duration

	// MaxOutputBytes bounds what one run may print. Zero means
	// DefaultMaxOutputBytes.
	MaxOutputBytes int

	// MemoryLimitPages bounds the linear memory of the guest, in pages of
	// 64 KiB. Zero means DefaultMemoryLimitPages.
	MemoryLimitPages uint32
}

// Execute instantiates the module, which runs its _start, and returns what it
// printed.
//
// It returns a *FailedError when the program exited non-zero, ErrTimeout when the
// deadline passed, and a plain error when the module could not be loaded at all.
func (e *Executor) Execute(ctx context.Context, module []byte) (*Result, error) {
	deadline, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()

	// WithCloseOnContextDone is what makes the deadline real. Without it a module
	// in a tight loop never yields and the timeout is a suggestion.
	runtime := wazero.NewRuntimeWithConfig(deadline,
		wazero.NewRuntimeConfig().
			WithCloseOnContextDone(true).
			WithMemoryLimitPages(e.memoryLimitPages()))
	defer runtime.Close(deadline)

	if _, err := wasi_snapshot_preview1.Instantiate(deadline, runtime); err != nil {
		return nil, fmt.Errorf("could not start the WASI runtime: %w", err)
	}

	// Standard output and standard error share one buffer. A panic prints to
	// stderr, and to a person reading the playground both are simply what the
	// program said, in the order it said it.
	output := &limited.Buffer{Limit: e.maxOutputBytes()}

	// Nothing else is granted. WithFSConfig is never called, so the guest sees no
	// file system, and no socket is ever opened for it.
	config := wazero.NewModuleConfig().
		WithStdout(output).
		WithStderr(output).
		WithArgs(programName).
		WithName("")

	// Loading and running are two steps on purpose. A module that will not load
	// is our problem — we produced it — while anything that goes wrong once it
	// runs belongs to the program. Merging the two into InstantiateWithConfig
	// makes a Rust panic indistinguishable from a corrupt module.
	compiled, err := runtime.CompileModule(deadline, module)
	if err != nil {
		return nil, fmt.Errorf("could not load the module: %w", err)
	}
	defer compiled.Close(deadline)

	started := time.Now()
	instance, err := runtime.InstantiateModule(deadline, compiled, config)
	elapsed := time.Since(started)

	if instance != nil {
		if closeErr := instance.Close(deadline); closeErr != nil && err == nil {
			err = closeErr
		}
	}

	if err != nil {
		return nil, e.failure(err, output)
	}

	return &Result{
		Output:    output.String(),
		Duration:  elapsed,
		Truncated: output.Truncated(),
	}, nil
}

// failure turns what wazero reported into the error this package promises.
//
// The module already loaded, so the program ran. wazero answers nil on exit code
// zero, which leaves three things: a deadline, an explicit non-zero exit, and a
// trap. All three are the program's, and none of them is ours.
func (e *Executor) failure(err error, output *limited.Buffer) error {
	failed := &FailedError{Output: output.String(), Truncated: output.Truncated()}

	var exit *sys.ExitError
	if !errors.As(err, &exit) {
		// A trap: unreachable, an access out of bounds, a division by zero. Rust
		// arrives here on every panic.
		failed.Reason = reasonOf(err)
		return failed
	}

	switch exit.ExitCode() {
	case sys.ExitCodeDeadlineExceeded:
		return ErrTimeout
	case sys.ExitCodeContextCanceled:
		return context.Canceled
	}

	failed.ExitCode = exit.ExitCode()
	return failed
}

// reasonOf shortens what wazero reported, for a person to read.
//
// The full text is a stack trace through the guest, and it opens with framing
// that means nothing to someone who wrote a Rust snippet:
//
//	module[] function[_start] failed: wasm error: unreachable
//	wasm stack trace: ...
//
// Only the trap itself is kept. Whatever the program said before it stopped is
// already in the output, and that is where the panic message lives. This
// shortens a message for display and never decides a kind — the structure above
// does that, so a reworded error cannot change how a failure is classified.
func reasonOf(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	if _, trap, found := strings.Cut(line, "wasm error: "); found {
		return trap
	}
	return line
}

func (e *Executor) timeout() time.Duration {
	if e.Timeout <= 0 {
		return DefaultTimeout
	}
	return e.Timeout
}

func (e *Executor) memoryLimitPages() uint32 {
	if e.MemoryLimitPages == 0 {
		return DefaultMemoryLimitPages
	}
	return e.MemoryLimitPages
}

func (e *Executor) maxOutputBytes() int {
	if e.MaxOutputBytes <= 0 {
		return DefaultMaxOutputBytes
	}
	return e.MaxOutputBytes
}
