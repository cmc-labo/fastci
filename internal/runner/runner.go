// Package runner executes a test-runner command line, streaming output
// straight through to the caller's terminal. It has no knowledge of which
// language or test runner is being invoked - each analyzer builds its own
// argv (go test, jest, ...) and hands it to Run.
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Options configures a test run.
type Options struct {
	Dir  string   // working directory to run the command in
	Argv []string // full command line, e.g. ["go", "test", "./..."]
}

// killGrace is how long a cancelled run is given to shut down after
// SIGTERM before it's forced with SIGKILL.
const killGrace = 10 * time.Second

// Run executes opts.Argv and returns the command's error (nil on success,
// *exec.ExitError on test failure).
//
// The command runs in its own process group so that, when ctx is
// cancelled, the shutdown signal reaches every process it spawned - not
// just the immediate child. This matters because none of the runners fastci
// drives are leaf processes: `go test` execs a separate test binary, jest
// and vitest fork worker processes, pytest and cargo test can too. Without
// this, cancelling fastci (a CI job cancellation, a timeout, Ctrl+C without
// a controlling terminal's own job control to fall back on) would leave
// those children running as orphans.
func Run(ctx context.Context, opts Options) error {
	if len(opts.Argv) == 0 {
		return fmt.Errorf("runner: empty command")
	}
	cmd := exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = killGrace
	return cmd.Run()
}
