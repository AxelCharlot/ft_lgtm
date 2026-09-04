// Package compile turns Rust source into a WebAssembly module.
//
// One rustc call is enough: there is no Cargo project, and wasm32-wasip1 links
// with the bundled rust-lld, so the image needs no other linker. See plan.md
// section V.1.2.
//
// rustc itself is not sandboxed. Rust runs code at compile time through proc
// macros and include_str!, so the limits here — a deadline, a directory of its
// own, a bare environment and a bound on what it may print — are the first line
// of defence and not a convenience. plan.md section V.1.3 carries the rest.
package compile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"gitlab.com/42schoolproject/postcommoncore/ft_lgtm/backend/internal/limited"
)

const (
	// DefaultTimeout bounds one rustc call. It is deliberately shorter than the
	// timeout of the whole request, so a slow compile is reported as a compile
	// timeout rather than as a request that died with nothing to say.
	DefaultTimeout = 10 * time.Second

	// MaxOutputBytes bounds what is kept of the compiler diagnostics. A source
	// built to make rustc talk for ever should not become memory in this process.
	MaxOutputBytes = 64 * 1024

	sourceFileName = "main.rs"
	moduleFileName = "out.wasm"
	target         = "wasm32-wasip1"
	edition        = "2021"
)

// ErrTimeout says the compiler ran past its deadline.
var ErrTimeout = errors.New("the compiler ran past its deadline")

// passedVariables are the only variables the compiler inherits from this
// process. Everything else stops here: the OTLP endpoint, the IPFS URLs, and
// whatever a manifest adds to the pod later.
//
// It is not empty, because rustc on the PATH is a rustup proxy and finds its
// toolchain through RUSTUP_HOME and CARGO_HOME. Without them it exits 1 and
// prints nothing, which reads exactly like a source that does not compile.
var passedVariables = []string{"PATH", "RUSTUP_HOME", "CARGO_HOME"}

// FailedError carries what rustc printed when it refused the source. The text is
// the compiler's own, and it reaches the user unchanged.
type FailedError struct {
	Output string
}

func (e *FailedError) Error() string {
	return "the source did not compile"
}

// Result is a module and the time it took to produce.
type Result struct {
	Module   []byte
	Duration time.Duration
}

// Compiler runs rustc. The zero value works and uses rustc from the PATH with
// DefaultTimeout.
type Compiler struct {
	// Path of the rustc binary. Empty means "rustc", found on the PATH.
	Path string

	// Timeout bounds one call. Zero means DefaultTimeout.
	Timeout time.Duration
}

// Compile writes the source into a directory of its own, calls rustc once, and
// returns the module it produced. The directory is removed before Compile
// returns, whether it succeeded or not.
//
// It returns a *FailedError when rustc refused the source, ErrTimeout when the
// deadline passed, and a plain error when something on this side went wrong.
func (c *Compiler) Compile(ctx context.Context, source string) (*Result, error) {
	directory, err := os.MkdirTemp("", "lgtm-compile-")
	if err != nil {
		return nil, fmt.Errorf("could not create the build directory: %w", err)
	}
	// The pod has one writable directory. A run that leaves its files behind
	// fills it, and the failure then lands on some later, innocent request.
	defer os.RemoveAll(directory)

	if err := os.WriteFile(filepath.Join(directory, sourceFileName), []byte(source), 0o600); err != nil {
		return nil, fmt.Errorf("could not write the source: %w", err)
	}

	deadline, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	output := &limited.Buffer{Limit: MaxOutputBytes}
	command := c.command(deadline, directory)
	command.Stdout = output
	command.Stderr = output

	started := time.Now()
	err = command.Run()
	elapsed := time.Since(started)

	if errors.Is(deadline.Err(), context.DeadlineExceeded) {
		return nil, ErrTimeout
	}
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return nil, &FailedError{Output: output.String()}
		}
		return nil, fmt.Errorf("could not run the compiler: %w", err)
	}

	module, err := os.ReadFile(filepath.Join(directory, moduleFileName))
	if err != nil {
		return nil, fmt.Errorf("the compiler reported success and wrote no module: %w", err)
	}
	return &Result{Module: module, Duration: elapsed}, nil
}

func (c *Compiler) command(ctx context.Context, directory string) *exec.Cmd {
	command := exec.CommandContext(ctx, c.path(),
		"--edition", edition,
		"--target", target,
		"-O",
		sourceFileName,
		"-o", moduleFileName,
	)
	command.Dir = directory

	command.Env = environment(directory)

	// rustc spawns rust-lld. Without a process group, the deadline kills rustc
	// and leaves the linker running, holding the directory this function is
	// about to delete.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	command.WaitDelay = time.Second

	return command
}

// environment builds what the compiler is allowed to see. TMPDIR points inside
// the build directory, so anything rustc writes on its own is removed with it.
func environment(directory string) []string {
	variables := []string{"TMPDIR=" + directory}
	for _, name := range passedVariables {
		if value, found := os.LookupEnv(name); found {
			variables = append(variables, name+"="+value)
		}
	}
	return variables
}

func (c *Compiler) path() string {
	if c.Path == "" {
		return "rustc"
	}
	return c.Path
}

func (c *Compiler) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}
