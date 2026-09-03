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
	// UncertainTargets is the sorted list of node IDs included in Targets
	// because they (or something they statically import) has a dynamic
	// import analysis couldn't resolve (graph.Node.HasDynamicImport) - not
	// because they or anything they depend on literally changed. Disjoint
	// from ChangedTargets and from any target already reachable through an
	// actual change. Always empty when FullRun is true.
	UncertainTargets []string
	// Reasons maps every node ID reachable from a change or an uncertain
	// seed (not just the ones with test files, i.e. a superset of Targets)
	// to the chain of node IDs explaining why: the seed itself (a changed
	// node, or a node with an unresolvable dynamic import) first, then each
	// node that imports the previous one, ending with the node in
	// question. A node absent from Reasons was not reached at all - nothing
	// in changedFiles affects it through the dependency graph. Always empty
	// when FullRun is true, since every target runs regardless of why.
	Reasons map[string][]string
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

	affected, parents := bfsReverse(g, changedTargets)

	// Nodes whose static import graph is known-incomplete must be treated as
	// possibly affected by *any* change, since we can't prove the changed
	// file isn't their unresolved dynamic target. Run a second reverse-edge
	// BFS seeded from those nodes - reusing bfsReverse unchanged, so
	// anything that statically imports one inherits the uncertainty for
	// free via the existing traversal - and union it into affected. Kept
	// separate from the change-driven BFS above so a target already pulled
	// in by a real change is reported as such, not as merely "uncertain".
	uncertainSeeds := map[string]bool{}
	for id, n := range g.Nodes {
		if n.HasDynamicImport {
			uncertainSeeds[id] = true
		}
	}
	var uncertain []string
	if len(uncertainSeeds) > 0 {
		affectedByUncertainty, uncertainParents := bfsReverse(g, uncertainSeeds)
		for id := range affectedByUncertainty {
			if affected[id] {
				continue // already reached by a real change; that's the more useful reason to report.
			}
			if n := g.Nodes[id]; n != nil && n.HasTestFiles {
				uncertain = append(uncertain, id)
			}
			affected[id] = true
			parents[id] = uncertainParents[id] // "" for the seed itself, matching bfsReverse's own convention.
		}
		sort.Strings(uncertain)
	}

	var targets []string
	reasons := make(map[string][]string, len(affected))
	for t := range affected {
		reasons[t] = reasonChain(parents, t)
		if n := g.Nodes[t]; n != nil && n.HasTestFiles {
			targets = append(targets, t)
		}
	}
	sort.Strings(targets)

	return Result{
		Targets:          targets,
		ChangedTargets:   sortedKeys(changedTargets),
		UncertainTargets: uncertain,
		Reasons:          reasons,
	}
}

// bfsReverse walks the reverse dependency graph starting from seeds,
// returning every target reachable by "is imported by" edges - i.e. every
// target that changing a seed could possibly affect - along with parents,
// mapping each reached node to the node one hop closer to its seed ("" for
// a seed itself), so the shortest path back to a seed can be reconstructed
// via reasonChain.
func bfsReverse(g *graph.Graph, seeds map[string]bool) (affected map[string]bool, parents map[string]string) {
	affected = make(map[string]bool, len(seeds))
	parents = make(map[string]string, len(seeds))
	queue := make([]string, 0, len(seeds))
	for t := range seeds {
		affected[t] = true
		parents[t] = ""
		queue = append(queue, t)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, importer := range g.Importers[cur] {
			if !affected[importer] {
				affected[importer] = true
				parents[importer] = cur
				queue = append(queue, importer)
			}
		}
	}
	return affected, parents
}

// reasonChain reconstructs the path from id's seed (the node with parents[x]
// == "") through to id itself, in seed-to-target order.
func reasonChain(parents map[string]string, id string) []string {
	var chain []string
	for {
		chain = append(chain, id)
		parent, ok := parents[id]
		if !ok || parent == "" {
			break
		}
		id = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
