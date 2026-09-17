package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/hpscript/fastci/internal/ciworkflow"
	"github.com/hpscript/fastci/internal/gitdiff"
)

func newLocalCmd() *cobra.Command {
	var opts testOpts

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
			opts.extraArgs = args
			return runLocal(cmd, opts)
		},
	}

	cmd.Flags().BoolVar(&opts.noCache, "no-cache", false,
		"always actually run every selected target, bypassing the local test-result cache")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print the selected test targets without running them")
	cmd.Flags().BoolVar(&opts.networkReport, "network-report", false,
		"run the test/build command through a local logging proxy and report which hosts it contacted afterward - see \"fastci test --help\"")

	return cmd
}

func runLocal(cmd *cobra.Command, opts testOpts) error {
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

	opts.base = "origin/" + det.Branch
	opts.verbose = true
	fmt.Printf("fastci: reproducing CI locally against %s (%s)\n", opts.base, det.Reason)

	return runTest(cmd, opts)
}
