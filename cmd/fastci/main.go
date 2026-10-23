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
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"
)

// version is set at release build time via
// -ldflags "-X main.version=v1.2.3"; see .goreleaser.yaml.
var version = ""

// resolvedVersion returns the release version, falling back to the module
// version `go install ...@vX.Y.Z` records, and to "dev" for a local build.
func resolvedVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

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
		Version:       resolvedVersion(),
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newTestCmd())
	root.AddCommand(newAnalyzeCmd())
	root.AddCommand(newGuardCmd())
	root.AddCommand(newLocalCmd())
	return root
}
