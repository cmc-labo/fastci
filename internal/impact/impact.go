// Package impact determines which test targets are affected by a given set
// of changed files, given a dependency graph and the analyzer that built
// it.
package impact

import (
	"sort"

	"github.com/hpscript/fastci/internal/analyzer"
	"github.com/hpscript/fastci/internal/graph"
)

// Result is the outcome of an impact analysis.
type Result struct {
	// Targets is the sorted list of node IDs that should be tested.
	Targets []string
	// ChangedTargets is the sorted list of node IDs that changed directly
	// (a subset of Targets, unless they have no test files).
	ChangedTargets []string
	// FullRun is true when the analysis could not safely narrow the test
	// set (e.g. a manifest/lockfile changed, or a changed source file
	// couldn't be resolved to a node) and every test target should run
	// instead.
	FullRun bool
	// FullRunReasons lists the changed files that forced a full run.
	FullRunReasons []string
}

// Compute determines the set of test targets affected by changedFiles
// (absolute paths) within the graph g, using a to classify files that
// don't resolve to a graph node.
func Compute(g *graph.Graph, changedFiles []string, a analyzer.Analyzer) Result {
	changedTargets := map[string]bool{}
	var unresolved []string
	var fullRunReasons []string

	for _, f := range changedFiles {
		if a.FullRunFile(f) {
			fullRunReasons = append(fullRunReasons, f)
			continue
		}
		if t, ok := g.TargetForFile(f); ok {
			changedTargets[t] = true
			continue
		}
		if a.Ignorable(f) {
			// Outside the analyzer's tracked source set (docs, assets,
			// etc.) - safe to ignore, it can't affect the graph.
			continue
		}
		unresolved = append(unresolved, f)
	}

	if len(unresolved) > 0 {
		fullRunReasons = append(fullRunReasons, unresolved...)
	}

	if len(fullRunReasons) > 0 {
		return Result{
			Targets:        g.TestNodeIDs(),
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
func bfsReverse(g *graph.Graph, seeds map[string]bool) map[string]bool {
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

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
