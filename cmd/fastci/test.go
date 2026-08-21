package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hpscript/fastci/internal/analyzer"
	"github.com/hpscript/fastci/internal/analyzer/cargoanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/goanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/jestanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/pytestanalyzer"
	"github.com/hpscript/fastci/internal/gitdiff"
	"github.com/hpscript/fastci/internal/impact"
)

// candidateAnalyzers lists every built-in analyzer, tried in order against
// the working directory until one reports it can handle the project.
func candidateAnalyzers() []analyzer.Analyzer {
	return []analyzer.Analyzer{
		goanalyzer.New(),
		jestanalyzer.New(),
		pytestanalyzer.New(),
		cargoanalyzer.New(),
	}
}

func newTestCmd() *cobra.Command {
	var (
		base    string
		dryRun  bool
		all     bool
		verbose bool
	)

	cmd := &cobra.Command{
		Use:   "test [-- test runner flags]",
		Short: "Run only the tests affected by the current change",
		Long: `test analyzes the current git diff, builds a dependency graph for the
project in the working directory, and runs its test runner only against
the packages/files that changed or transitively depend on something that
changed.

The project type is auto-detected: a Go module or workspace (go.mod /
go.work), a Jest-based TypeScript/JavaScript project (package.json with
Jest configured), a pytest-based Python project (pytest.ini, conftest.py,
or a "[tool.pytest.ini_options]"/"[tool:pytest]" section), or a Rust crate
or Cargo workspace (Cargo.toml).

Flags after "--" are forwarded to the underlying test runner unchanged,
e.g.:

  fastci test -- -v -race        # go test
  fastci test -- --coverage      # jest
  fastci test -- -x -k foo       # pytest
  fastci test -- --no-fail-fast  # cargo test`,
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

	a, err := analyzer.Detect(cwd, candidateAnalyzers())
	if err != nil {
		return err
	}

	repoRoot, err := gitdiff.RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("resolving git repository root: %w", err)
	}

	if opts.all {
		fmt.Printf("fastci: --all set, running the full test suite (%s)\n", a.Name())
		if opts.dryRun {
			fmt.Println("fastci: dry-run, not executing tests")
			return nil
		}
		allTargets, err := a.AllTargets(cwd)
		if err != nil {
			return err
		}
		return a.RunTests(cmd.Context(), cwd, allTargets, opts.extraArgs)
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

	g, err := a.Build(cwd)
	if err != nil {
		return fmt.Errorf("building dependency graph: %w", err)
	}

	result := impact.Compute(g, changed, a)
	total := len(g.TestNodeIDs())

	if result.FullRun {
		fmt.Printf("fastci: could not safely narrow the test set, running the full suite (%s). Reason(s):\n", a.Name())
		for _, r := range result.FullRunReasons {
			fmt.Printf("  - %s\n", relOrSelf(repoRoot, r))
		}
		if opts.dryRun {
			fmt.Println("fastci: dry-run, not executing tests")
			return nil
		}
		return a.RunTests(cmd.Context(), cwd, result.Targets, opts.extraArgs)
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
	fmt.Printf("fastci: selected %d/%d test target(s) (%s, %.0f%% skipped)\n", len(result.Targets), total, a.Name(), pct)
	changedSet := make(map[string]bool, len(result.ChangedTargets))
	for _, t := range result.ChangedTargets {
		changedSet[t] = true
	}
	uncertainSet := make(map[string]bool, len(result.UncertainTargets))
	for _, t := range result.UncertainTargets {
		uncertainSet[t] = true
	}
	if len(result.UncertainTargets) > 0 {
		fmt.Printf("fastci: %d target(s) below (marked ~) contain an import fastci can't statically resolve, so they're always run as a safety net rather than only when something they depend on changed. Converting to a static import (or a moduleNameMapper/tsconfig alias, for Jest) regains precision.\n", len(result.UncertainTargets))
	}
	for _, t := range result.Targets {
		marker := " "
		switch {
		case changedSet[t]:
			marker = "*"
		case uncertainSet[t]:
			marker = "~"
		}
		fmt.Printf("  %s %s\n", marker, relOrSelf(repoRoot, t))
	}

	if opts.dryRun {
		fmt.Println("fastci: dry-run, not executing tests")
		return nil
	}

	return a.RunTests(cmd.Context(), cwd, result.Targets, opts.extraArgs)
}

func relOrSelf(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
