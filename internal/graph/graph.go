// Package graph holds the language-agnostic dependency graph shape shared
// by every fastci analyzer. An analyzer (Go, Jest, ...) is responsible for
// deciding what a "node" is - a package import path for Go, a source file
// for Jest - and for populating a Graph accordingly; everything downstream
// (impact analysis, reporting) only depends on this shape.
package graph

import (
	"path/filepath"
	"sort"
)

// Node is one test-able unit: for Go, a package/import path; for
// file-granularity analyzers like Jest, a single source file.
type Node struct {
	ID           string
	Files        []string
	HasTestFiles bool
	// HasDynamicImport marks a node whose true import set isn't fully
	// captured by Imports - e.g. a dynamic import()/require() call or
	// importlib.import_module() whose target can't be resolved statically.
	// Some change elsewhere in the project might affect this node in a way
	// the graph can't see, so impact analysis treats it as always possibly
	// affected rather than silently ignoring the gap.
	HasDynamicImport bool
	Imports          map[string]bool // IDs of other Nodes this one depends on
}

// Graph is a full dependency graph produced by an analyzer.
type Graph struct {
	// Nodes maps node ID -> Node.
	Nodes map[string]*Node
	// Importers maps node ID -> IDs of nodes that directly depend on it
	// (the reverse of Node.Imports). Populated by BuildImporters.
	Importers map[string][]string

	fileToNode map[string]string
	dirToNodes map[string][]string
}

// New returns an empty Graph ready for an analyzer to populate.
func New() *Graph {
	return &Graph{
		Nodes:      map[string]*Node{},
		Importers:  map[string][]string{},
		fileToNode: map[string]string{},
		dirToNodes: map[string][]string{},
	}
}

// Node returns the Node for id, creating it if necessary.
func (g *Graph) Node(id string) *Node {
	n, ok := g.Nodes[id]
	if !ok {
		n = &Node{ID: id, Imports: map[string]bool{}}
		g.Nodes[id] = n
	}
	return n
}

// IndexFiles builds the file/directory lookup indexes used by
// TargetForFile. Call it once after every Node's Files slice is final.
func (g *Graph) IndexFiles() {
	for id, n := range g.Nodes {
		n.Files = dedup(n.Files)
		for _, f := range n.Files {
			g.fileToNode[f] = id
			dir := filepath.Dir(f)
			g.dirToNodes[dir] = appendUnique(g.dirToNodes[dir], id)
		}
	}
}

// BuildImporters populates Importers from every Node's Imports. Call it
// once after every Node's Imports set is final.
func (g *Graph) BuildImporters() {
	for id, n := range g.Nodes {
		for imp := range n.Imports {
			g.Importers[imp] = appendUnique(g.Importers[imp], id)
		}
	}
}

// TargetForFile resolves an absolute file path to the ID of the Node it
// belongs to. ok is false if the file isn't part of any known node (e.g.
// it's not a source file the analyzer tracks, or it was deleted and its
// directory no longer resolves to a single unambiguous node).
func (g *Graph) TargetForFile(absPath string) (id string, ok bool) {
	if t, ok := g.fileToNode[absPath]; ok {
		return t, true
	}
	dir := filepath.Dir(absPath)
	if targets := g.dirToNodes[dir]; len(targets) == 1 {
		return targets[0], true
	}
	return "", false
}

// TestNodeIDs returns the sorted IDs of every Node that has test files.
func (g *Graph) TestNodeIDs() []string {
	var out []string
	for id, n := range g.Nodes {
		if n.HasTestFiles {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func dedup(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}
