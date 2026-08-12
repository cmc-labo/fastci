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
)

// Options configures a test run.
type Options struct {
	Dir  string   // working directory to run the command in
	Argv []string // full command line, e.g. ["go", "test", "./..."]
}

// Run executes opts.Argv and returns the command's error (nil on success,
// *exec.ExitError on test failure).
func Run(ctx context.Context, opts Options) error {
	if len(opts.Argv) == 0 {
		return fmt.Errorf("runner: empty command")
	}
	cmd := exec.CommandContext(ctx, opts.Argv[0], opts.Argv[1:]...)
	cmd.Dir = opts.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
