package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hpscript/fastci/internal/depgraph"
	"github.com/hpscript/fastci/internal/gitdiff"
	"github.com/hpscript/fastci/internal/impact"
	"github.com/hpscript/fastci/internal/runner"
)

func newTestCmd() *cobra.Command {
	var (
		base    string
		dryRun  bool
		all     bool
		verbose bool
	)

	cmd := &cobra.Command{
		Use:   "test [-- go test flags]",
		Short: "Run only the tests affected by the current change",
		Long: `test analyzes the current git diff, builds a package dependency graph for
the Go module in the working directory, and runs "go test" only against the
packages that changed or transitively depend on something that changed.

Flags after "--" are forwarded to "go test" unchanged, e.g.:

  fastci test -- -v -race`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTest(cmd, testOpts{
				base:      base,
				dryRun:    dryRun,
				all:       all,
				verbose:   verbose,
				extraArgs: args,
			})
		},
	}

	cmd.Flags().StringVar(&base, "base", "", "git ref to diff against (three-dot merge-base diff). Defaults to comparing the working tree against HEAD.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the selected test targets without running them")
	cmd.Flags().BoolVar(&all, "all", false, "skip impact analysis and run the full test suite")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print the changed files and full selection reasoning")

	return cmd
}

type testOpts struct {
	base      string
	dryRun    bool
	all       bool
	verbose   bool
	extraArgs []string
}

func runTest(cmd *cobra.Command, opts testOpts) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Patterns doubles as our "is this a valid Go module or workspace root"
	// check: it fails clearly if cwd has neither a go.mod nor an active
	// go.work.
	allPatterns, err := depgraph.Patterns(cwd)
	if err != nil {
		return err
	}

	repoRoot, err := gitdiff.RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("resolving git repository root: %w", err)
	}

	if opts.all {
		fmt.Println("fastci: --all set, running the full test suite")
		if opts.dryRun {
			fmt.Println("fastci: dry-run, not executing go test")
			return nil
		}
		return runner.Run(cmd.Context(), runner.Options{Dir: cwd, Targets: allPatterns, ExtraArgs: opts.extraArgs})
	}

	changed, err := gitdiff.ChangedFiles(repoRoot, opts.base)
	if err != nil {
		return fmt.Errorf("computing changed files: %w", err)
	}
	if len(changed) == 0 {
		fmt.Println("fastci: no changed files detected, nothing to test")
		return nil
	}
	if opts.verbose {
		fmt.Printf("fastci: %d changed file(s):\n", len(changed))
		for _, f := range changed {
			fmt.Printf("  %s\n", relOrSelf(repoRoot, f))
		}
	}

	graph, err := depgraph.Load(cwd)
	if err != nil {
		return fmt.Errorf("building dependency graph: %w", err)
	}

	result := impact.Compute(graph, changed)
	total := countTestTargets(graph)

	if result.FullRun {
		fmt.Println("fastci: could not safely narrow the test set, running the full suite. Reason(s):")
		for _, r := range result.FullRunReasons {
			fmt.Printf("  - %s\n", relOrSelf(repoRoot, r))
		}
		if opts.dryRun {
			fmt.Println("fastci: dry-run, not executing go test")
			return nil
		}
		return runner.Run(cmd.Context(), runner.Options{Dir: cwd, Targets: allPatterns, ExtraArgs: opts.extraArgs})
	}

	if len(result.Targets) == 0 {
		fmt.Println("fastci: no test targets are affected by this change")
		return nil
	}

	skipped := total - len(result.Targets)
	pct := 0.0
	if total > 0 {
		pct = float64(skipped) / float64(total) * 100
	}
	fmt.Printf("fastci: selected %d/%d test package(s) (%.0f%% skipped)\n", len(result.Targets), total, pct)
	changedSet := make(map[string]bool, len(result.ChangedTargets))
	for _, t := range result.ChangedTargets {
		changedSet[t] = true
	}
	for _, t := range result.Targets {
		marker := " "
		if changedSet[t] {
			marker = "*"
		}
		fmt.Printf("  %s %s\n", marker, t)
	}

	if opts.dryRun {
		fmt.Println("fastci: dry-run, not executing go test")
		return nil
	}

	return runner.Run(cmd.Context(), runner.Options{Dir: cwd, Targets: result.Targets, ExtraArgs: opts.extraArgs})
}

func countTestTargets(g *depgraph.Graph) int {
	n := 0
	for _, node := range g.Nodes {
		if node.HasTestFiles {
			n++
		}
	}
	return n
}

func relOrSelf(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
