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

	"golang.org/x/sys/unix"
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
// Without a controlling terminal on stdin (the normal CI case - no TTY at
// all), the command runs in its own process group so that, when ctx is
// cancelled, the shutdown signal reaches every process it spawned, not just
// the immediate child. This matters because none of the runners fastci
// drives are leaf processes: `go test` execs a separate test binary, jest
// and vitest fork worker processes, pytest and cargo test can too. Without
// this, cancelling fastci (a CI job cancellation, a timeout, a supervisor
// sending SIGTERM directly to this process's PID) would leave those
// children running as orphans.
//
// With a real controlling terminal attached (interactive local use), the
// child instead stays in fastci's own process group, exactly as
// os/exec would do by default: the terminal's own job control already
// delivers Ctrl+C to the whole foreground group without any help here, so
// there's nothing to fix - and isolating the child into a background group
// in that case would actively break it, since a background process group
// that tries to read from the controlling terminal is stopped by SIGTTIN
// (hit by, e.g., a pytest test that drops into a debugger breakpoint),
// hanging both the child and fastci itself waiting on it.
func Run(ctx context.Context, opts Options) error {
	if len(opts.Argv) == 0 {
		return fmt.Errorf("runner: empty command")
	}
	cmd := exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if !IsTerminal(os.Stdin) {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
		cmd.WaitDelay = killGrace
	}

	return cmd.Run()
}

// IsTerminal reports whether f is connected to a controlling terminal.
func IsTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// OpenPTY allocates a fresh pseudo-terminal and returns its master end and
// the path to the corresponding slave device. Giving a child process the
// slave as its stdout/stderr (instead of a plain os.Pipe) lets it correctly
// detect a real terminal - many test runners (cargo test, pytest, jest,
// vitest) check this to decide whether to emit ANSI color, which a plain
// pipe would silently turn off.
func OpenPTY() (master *os.File, slavePath string, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, "", fmt.Errorf("runner: opening /dev/ptmx: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		return nil, "", fmt.Errorf("runner: unlocking pty: %w", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		return nil, "", fmt.Errorf("runner: reading pty number: %w", err)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n), nil
}
