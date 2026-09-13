package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	"github.com/hpscript/fastci/internal/testcache"
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
		why                 string
		noCache             bool
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
				why:                 why,
				noCache:             noCache,
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
	cmd.Flags().StringVar(&why, "why", "",
		"explain why the given file (or, for Go/Cargo, package import path/crate name) was or wasn't selected, showing the dependency chain back to the changed file responsible - or that no changed file reaches it at all. Diagnostic only: doesn't run any tests.")
	cmd.Flags().BoolVar(&noCache, "no-cache", false,
		"always actually run every selected target, bypassing the local test-result cache (.fastci-cache/test-results.json) that would otherwise skip re-running a target whose exact current content - its own files plus everything it transitively depends on - already passed in a previous run")

	return cmd
}

type testOpts struct {
	base                string
	dryRun              bool
	all                 bool
	verbose             bool
	fullRunThresholdPct float64
	why                 string
	noCache             bool
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

	if opts.all && opts.why != "" {
		// --why promises to be diagnostic-only; --all bypasses impact
		// analysis entirely, so there's no dependency-graph reasoning to
		// show and, without this check, --why would otherwise be silently
		// ignored while the full suite ran anyway.
		fmt.Printf("fastci: --all bypasses impact analysis entirely, so %q (like everything else) would run regardless of the dependency graph - nothing to explain\n", opts.why)
		return nil
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
		return runAndRecord(cmd.Context(), repoRoot, a, cwd, allTargets, opts.extraArgs)
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

	if opts.why != "" {
		return explainWhy(g, result, cwd, repoRoot, opts.why)
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
		return runSelectedTargets(cmd, repoRoot, g, a, cwd, result.Targets, opts)
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

	return runSelectedTargets(cmd, repoRoot, g, a, cwd, result.Targets, opts)
}

// runSelectedTargets runs targets - already narrowed by impact analysis, or
// every test target during a full run - after first filtering out any the
// local test-result cache (internal/testcache) already has a passing
// result for, under their exact current content. This is a genuinely
// different, more precise mechanism than the dependency-graph narrowing
// above: that narrowing decides "which targets could this diff possibly
// affect", while this cache asks "have we already seen this exact target
// content pass, regardless of how it got selected" - so it can still find
// something to skip even during a full run. --no-cache bypasses this
// entirely.
func runSelectedTargets(cmd *cobra.Command, repoRoot string, g *graph.Graph, a analyzer.Analyzer, cwd string, targets []string, opts testOpts) error {
	if opts.noCache {
		if opts.dryRun {
			fmt.Println("fastci: dry-run, not executing tests")
			return nil
		}
		return runAndRecord(cmd.Context(), repoRoot, a, cwd, targets, opts.extraArgs)
	}

	cache := testcache.Open(repoRoot)
	hits, misses := cache.Filter(g, a.Name(), opts.extraArgs, targets)
	if len(hits) > 0 {
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = relOrSelf(repoRoot, h)
		}
		fmt.Printf("fastci: %d/%d target(s) skipped (cache hit - unchanged content already passed): %s\n",
			len(hits), len(targets), strings.Join(names, ", "))
	}

	if opts.dryRun {
		fmt.Println("fastci: dry-run, not executing tests")
		return nil
	}

	if len(misses) == 0 {
		fmt.Println("fastci: nothing to run - every selected target was a cache hit")
		return nil
	}

	if err := runAndRecord(cmd.Context(), repoRoot, a, cwd, misses, opts.extraArgs); err != nil {
		return err
	}
	cache.RecordPass(g, a.Name(), opts.extraArgs, misses)
	if err := cache.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "fastci: could not save test-result cache: %v\n", err)
	}
	return nil
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

// explainWhy resolves query to a graph node (trying it first as a file path
// relative to cwd, then as a literal node ID - a Go import path or Cargo
// crate name, for analyzers where a node isn't a single file) and prints
// why it was, or wasn't, selected: the dependency chain back to whichever
// changed file (or unresolvable dynamic import) is responsible, or an
// explicit statement that no changed file reaches it at all.
func explainWhy(g *graph.Graph, result impact.Result, cwd, repoRoot, query string) error {
	target, ok := resolveQuery(g, cwd, query)
	if !ok {
		return fmt.Errorf("--why: %q doesn't match any tracked file or node in this project", query)
	}
	fmt.Printf("fastci: why is %q selected?\n", relOrSelf(repoRoot, target))

	if result.FullRun {
		fmt.Println("  --all/full-run is in effect, so every test target runs regardless of the dependency graph. Reason(s):")
		for _, r := range result.FullRunReasons {
			if filepath.IsAbs(r) {
				r = relOrSelf(repoRoot, r)
			}
			fmt.Printf("    - %s\n", r)
		}
		return nil
	}

	chain, ok := result.Reasons[target]
	if !ok {
		fmt.Println("  NOT selected: no changed file's effect reaches this target through the dependency graph.")
		return nil
	}

	changedSet := make(map[string]bool, len(result.ChangedTargets))
	for _, t := range result.ChangedTargets {
		changedSet[t] = true
	}
	uncertainSet := make(map[string]bool, len(result.UncertainTargets))
	for _, t := range result.UncertainTargets {
		uncertainSet[t] = true
	}

	if n := g.Nodes[target]; n == nil || !n.HasTestFiles {
		fmt.Println("  Affected, but has no test files of its own, so nothing actually runs for it - shown for context:")
	} else if changedSet[target] {
		fmt.Println("  Selected: it changed directly.")
		return nil
	} else if uncertainSet[target] {
		fmt.Println("  Selected as a safety net: it (or something it imports) contains an import fastci can't statically resolve, so it's always run when anything in the project changes:")
	} else {
		fmt.Println("  Selected because of this dependency chain:")
	}

	seed := chain[0]
	seedDesc := "changed"
	if g.Nodes[seed] != nil && g.Nodes[seed].HasDynamicImport && !changedSet[seed] {
		seedDesc = "unresolvable dynamic import"
	}
	for i, id := range chain {
		rel := relOrSelf(repoRoot, id)
		if i == 0 {
			fmt.Printf("    %s (%s)\n", rel, seedDesc)
			continue
		}
		fmt.Printf("    -> %s\n", rel)
	}
	return nil
}

// resolveQuery resolves a --why argument to a graph node ID: first as a
// file path (relative to cwd if not already absolute), then, if that
// doesn't match, as a literal node ID for analyzers whose nodes aren't
// individual files (a Go import path, a Cargo crate name).
func resolveQuery(g *graph.Graph, cwd, query string) (id string, ok bool) {
	abs := query
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	// A directory must resolve via TargetForDir, not TargetForFile: the
	// latter always strips one path component (filepath.Dir) expecting a
	// *file* path, so handing it a directory silently resolves to that
	// directory's *parent* package/crate instead - a real, silent wrong
	// answer for e.g. "--why internal/analyzer/cargoanalyzer" when Go's
	// tracked source set makes that path a directory, not a file.
	if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
		if id, ok := g.TargetForDir(abs); ok {
			return id, true
		}
	} else if id, ok := g.TargetForFile(abs); ok {
		return id, true
	}
	if _, ok := g.Nodes[query]; ok {
		return query, true
	}
	return "", false
}

func relOrSelf(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
