// Command fastci is a lightweight CI accelerator: it narrows a test run
// down to the packages actually affected by the current change, so it can
// sit in front of `go test` in a GitHub Actions workflow (or locally)
// without requiring a heavyweight build system like Bazel.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fastci:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "fastci",
		Short:         "Impact-driven test runner and CI accelerator",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newTestCmd())
	return root
}
