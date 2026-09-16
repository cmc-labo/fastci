package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hpscript/fastci/internal/ciworkflow"
	"github.com/hpscript/fastci/internal/gitdiff"
)

func newLocalCmd() *cobra.Command {
	var (
		noCache bool
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "local [-- test runner flags]",
		Short: "Reproduce CI's test run locally: auto-detect the PR base branch from .github/workflows and run `fastci test` against it",
		Long: `local inspects .github/workflows/*.yml for the base branch CI would diff a
pull request against - from an explicit "on.pull_request.branches", falling
back to "on.push.branches" (common, since a bare "pull_request:" trigger
with no branches filter is normal: GitHub already scopes it to the PR's own
base branch), falling back to the repository's actual default branch, and
finally to "main" - then runs "fastci test --base origin/<branch>" with
that base, so a local run narrows down to the same set of affected tests
CI would.

Flags after "--" are forwarded to the underlying test runner unchanged, the
same as "fastci test".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLocal(cmd, args, noCache, dryRun)
		},
	}

	cmd.Flags().BoolVar(&noCache, "no-cache", false,
		"always actually run every selected target, bypassing the local test-result cache")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the selected test targets without running them")

	return cmd
}

func runLocal(cmd *cobra.Command, extraArgs []string, noCache, dryRun bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	repoRoot, err := gitdiff.RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("resolving git repository root: %w", err)
	}

	det, err := ciworkflow.Detect(repoRoot)
	if err != nil {
		return fmt.Errorf("detecting CI's base branch: %w", err)
	}

	base := "origin/" + det.Branch
	fmt.Printf("fastci: reproducing CI locally against %s (%s)\n", base, det.Reason)

	return runTest(cmd, testOpts{
		base:      base,
		verbose:   true,
		noCache:   noCache,
		dryRun:    dryRun,
		extraArgs: extraArgs,
	})
}
