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
	"github.com/hpscript/fastci/internal/analyzer/vitestanalyzer"
	"github.com/hpscript/fastci/internal/gitdiff"
	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/impact"
)

// candidateAnalyzers lists every built-in analyzer, tried in order against
// the working directory until one reports it can handle the project.
// vitestanalyzer is tried before jestanalyzer: its Detect only matches on a
// vitest.config.* file or a "vitest" dependency, both specific enough signals
// that they should win even if a project also happens to have a stale
// "jest" devDependency left over from a migration; a plain Jest project
// matches neither and falls through to jestanalyzer as before.
func candidateAnalyzers() []analyzer.Analyzer {
	return []analyzer.Analyzer{
		goanalyzer.New(),
		vitestanalyzer.New(),
		jestanalyzer.New(),
		pytestanalyzer.New(),
		cargoanalyzer.New(),
	}
}

func newTestCmd() *cobra.Command {
	var (
		base                string
		dryRun              bool
		all                 bool
		verbose             bool
		fullRunThresholdPct float64
	)

	cmd := &cobra.Command{
		Use:   "test [-- test runner flags]",
		Short: "Run only the tests affected by the current change",
		Long: `test analyzes the current git diff, builds a dependency graph for the
project in the working directory, and runs its test runner only against
the packages/files that changed or transitively depend on something that
changed.

The project type is auto-detected: a Go module or workspace (go.mod /
go.work), a Vitest-based TypeScript/JavaScript project (vitest.config.* or
a "vitest" dependency), a Jest-based TypeScript/JavaScript project
(package.json with Jest configured), a pytest-based Python project
(pytest.ini, conftest.py, or a "[tool.pytest.ini_options]"/"[tool:pytest]"
section), or a Rust crate or Cargo workspace (Cargo.toml).

Flags after "--" are forwarded to the underlying test runner unchanged,
e.g.:

  fastci test -- -v -race        # go test
  fastci test -- --coverage      # vitest/jest
  fastci test -- -x -k foo       # pytest
  fastci test -- --no-fail-fast  # cargo test`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTest(cmd, testOpts{
				base:                base,
				dryRun:              dryRun,
				all:                 all,
				verbose:             verbose,
				fullRunThresholdPct: fullRunThresholdPct,
				extraArgs:           args,
			})
		},
	}

	cmd.Flags().StringVar(&base, "base", "", "git ref to diff against (three-dot merge-base diff). Defaults to comparing the working tree against HEAD.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the selected test targets without running them")
	cmd.Flags().BoolVar(&all, "all", false, "skip impact analysis and run the full test suite")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print the changed files and full selection reasoning")
	cmd.Flags().Float64Var(&fullRunThresholdPct, "full-run-threshold", 0,
		"if this many percent (0-100) of tracked source files changed, run the full suite instead of narrowing - a safety net against a diff too broad for per-file attribution to be meaningful. A change transitively affecting many tests through the dependency graph (e.g. a shared core library) is already handled precisely without this flag; it exists for diffs so broad that narrowing itself is the risk. 0 (default) disables this check.")

	return cmd
}

type testOpts struct {
	base                string
	dryRun              bool
	all                 bool
	verbose             bool
	fullRunThresholdPct float64
	extraArgs           []string
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

	if !result.FullRun && opts.fullRunThresholdPct > 0 {
		if reason, ok := fullRunThresholdReason(g, changed, a, opts.fullRunThresholdPct); ok {
			result.FullRun = true
			result.FullRunReasons = []string{reason}
		}
	}

	if result.FullRun {
		fmt.Printf("fastci: could not safely narrow the test set, running the full suite (%s). Reason(s):\n", a.Name())
		for _, r := range result.FullRunReasons {
			// Most reasons are absolute file paths (e.g. a manifest that
			// forced a full run); the threshold check's reason is a plain
			// sentence instead, so only relativize actual paths.
			if filepath.IsAbs(r) {
				r = relOrSelf(repoRoot, r)
			}
			fmt.Printf("  - %s\n", r)
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
		hint := "Converting it to a statically-resolvable import regains precision."
		if a.Name() == "jest" {
			hint = "Converting it to a static import (or a moduleNameMapper/tsconfig alias) regains precision."
		}
		fmt.Printf("fastci: %d target(s) below (marked ~) contain an import fastci can't statically resolve, so they're always run as a safety net rather than only when something they depend on changed. %s\n", len(result.UncertainTargets), hint)
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

// fullRunThresholdReason reports whether the fraction of changed files that
// are actually part of the analyzer's tracked source set (i.e. excluding
// docs/config/other files Ignorable already treats as inert) meets or
// exceeds thresholdPct percent of every tracked source file in the graph.
// This is a blunt, file-count-based safety net independent of the
// dependency graph itself - for a diff broad enough (e.g. a mass reformat
// or a large refactor), per-file impact attribution carries more risk of a
// subtle miss than it saves in narrowing, so it's simpler and safer to just
// run everything. It does not affect the common case of a change to one
// widely-depended-on file (e.g. a shared core library): that's already
// resolved precisely via the reverse-dependency graph, which naturally
// selects every test actually at risk without needing this flag at all.
func fullRunThresholdReason(g *graph.Graph, changed []string, a analyzer.Analyzer, thresholdPct float64) (reason string, ok bool) {
	totalFiles := 0
	for _, n := range g.Nodes {
		totalFiles += len(n.Files)
	}
	if totalFiles == 0 {
		return "", false
	}

	trackedChanged := 0
	for _, f := range changed {
		if !a.Ignorable(f) {
			trackedChanged++
		}
	}
	if trackedChanged == 0 {
		return "", false
	}

	pct := float64(trackedChanged) / float64(totalFiles) * 100
	if pct < thresholdPct {
		return "", false
	}
	return fmt.Sprintf("%d/%d tracked source file(s) changed (%.0f%%), at or above --full-run-threshold=%.0f%%",
		trackedChanged, totalFiles, pct, thresholdPct), true
}

func relOrSelf(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
