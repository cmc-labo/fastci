// Package runner executes `go test` against a chosen set of package import
// paths, streaming output straight through to the caller's terminal.
package runner

import (
	"context"
	"os"
	"os/exec"
)

// Options configures a test run.
type Options struct {
	Dir       string   // working directory to run `go test` in
	Targets   []string // import paths to test
	ExtraArgs []string // additional args forwarded to `go test`, e.g. -v, -race
}

// Run executes `go test` against opts.Targets and returns the command's
// error (nil on success, *exec.ExitError on test failure).
func Run(ctx context.Context, opts Options) error {
	args := append([]string{"test"}, opts.ExtraArgs...)
	args = append(args, opts.Targets...)

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = opts.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
