// Command fastci is a lightweight CI accelerator: it narrows a test run
// down to the packages actually affected by the current change, so it can
// sit in front of `go test` in a GitHub Actions workflow (or locally)
// without requiring a heavyweight build system like Bazel.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
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
