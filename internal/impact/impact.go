// Package impact determines which test targets are affected by a given set
// of changed files, given a package dependency graph.
package impact

import (
	"path/filepath"
	"sort"

	"github.com/hpscript/fastci/internal/depgraph"
)

// Result is the outcome of an impact analysis.
type Result struct {
	// Targets is the sorted list of import paths that should be tested.
	Targets []string
	// ChangedTargets is the sorted list of import paths that changed
	// directly (a subset of Targets, unless they have no test files).
	ChangedTargets []string
	// FullRun is true when the analysis could not safely narrow the test
	// set (e.g. go.mod changed, or a changed .go file couldn't be resolved
	// to a package) and every test target should run instead.
	FullRun bool
	// FullRunReasons lists the changed files that forced a full run.
	FullRunReasons []string
}

// Compute determines the set of test targets affected by changedFiles
// (absolute paths) within the module described by g.
func Compute(g *depgraph.Graph, changedFiles []string) Result {
	changedTargets := map[string]bool{}
	var unresolvedGo []string
	var fullRunReasons []string

	for _, f := range changedFiles {
		base := filepath.Base(f)
		if base == "go.mod" || base == "go.sum" || base == "go.work" || base == "go.work.sum" {
			fullRunReasons = append(fullRunReasons, f)
			continue
		}
		if filepath.Ext(f) != ".go" {
			// Non-Go changes (docs, workflow YAML, etc.) don't affect the
			// package graph, so they're safe to ignore.
			continue
		}
		if t, ok := g.TargetForFile(f); ok {
			changedTargets[t] = true
			continue
		}
		unresolvedGo = append(unresolvedGo, f)
	}

	if len(unresolvedGo) > 0 {
		fullRunReasons = append(fullRunReasons, unresolvedGo...)
	}

	if len(fullRunReasons) > 0 {
		return Result{
			Targets:        allTestTargets(g),
			FullRun:        true,
			FullRunReasons: fullRunReasons,
		}
	}

	affected := bfsReverse(g, changedTargets)

	var targets []string
	for t := range affected {
		if n := g.Nodes[t]; n != nil && n.HasTestFiles {
			targets = append(targets, t)
		}
	}
	sort.Strings(targets)

	return Result{
		Targets:        targets,
		ChangedTargets: sortedKeys(changedTargets),
	}
}

// bfsReverse walks the reverse dependency graph starting from seeds,
// returning every target reachable by "is imported by" edges - i.e. every
// target that changing a seed could possibly affect.
func bfsReverse(g *depgraph.Graph, seeds map[string]bool) map[string]bool {
	affected := make(map[string]bool, len(seeds))
	queue := make([]string, 0, len(seeds))
	for t := range seeds {
		affected[t] = true
		queue = append(queue, t)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, importer := range g.Importers[cur] {
			if !affected[importer] {
				affected[importer] = true
				queue = append(queue, importer)
			}
		}
	}
	return affected
}

func allTestTargets(g *depgraph.Graph) []string {
	var out []string
	for path, n := range g.Nodes {
		if n.HasTestFiles {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
