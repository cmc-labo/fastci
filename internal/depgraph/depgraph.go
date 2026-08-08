// Package depgraph builds a package-level dependency graph for a Go module,
// keyed by the import path you would pass to `go test`.
//
// It is built on top of golang.org/x/tools/go/packages with Tests:true,
// which yields several synthetic package variants per directory (the plain
// production package, an internal test-augmented package, an external
// "_test" package, and a synthetic test-binary main package). This package
// collapses all of those back down into one Node per test target, since
// that's the unit fastci ultimately runs `go test` against.
package depgraph

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Node is one `go test`-able target: a single directory/import path, with
// its production and test files merged together.
type Node struct {
	ImportPath   string
	Files        []string
	HasTestFiles bool
	Imports      map[string]bool // import paths of other Nodes this one depends on
}

// Graph is the full dependency graph for a module.
type Graph struct {
	ModulePath string
	// Nodes maps import path -> Node.
	Nodes map[string]*Node
	// Importers maps import path -> import paths of Nodes that directly
	// depend on it (the reverse of Node.Imports).
	Importers map[string][]string
	// fileToTarget maps absolute file path -> owning import path.
	fileToTarget map[string]string
	// dirToTargets maps absolute directory path -> import paths rooted there.
	// Used as a fallback when a changed file no longer exists on disk (e.g.
	// it was deleted) and so can't be resolved via fileToTarget.
	dirToTargets map[string][]string
}

// Load builds the dependency graph for the Go module rooted at dir by
// invoking `go list` (via golang.org/x/tools/go/packages) over ./....
func Load(dir string) (*Graph, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedModule,
		Dir:   dir,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("depgraph: loading packages: %w", err)
	}

	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("depgraph: %d package load error(s), e.g. %s", len(loadErrs), loadErrs[0])
	}

	g := &Graph{
		Nodes:        map[string]*Node{},
		Importers:    map[string][]string{},
		fileToTarget: map[string]string{},
		dirToTargets: map[string][]string{},
	}

	getNode := func(importPath string) *Node {
		n, ok := g.Nodes[importPath]
		if !ok {
			n = &Node{ImportPath: importPath, Imports: map[string]bool{}}
			g.Nodes[importPath] = n
		}
		return n
	}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Module == nil || !p.Module.Main {
			return // stdlib or third-party dependency: not part of the module under test
		}
		if g.ModulePath == "" {
			g.ModulePath = p.Module.Path
		}
		if isTestBinaryMain(p) {
			return // synthetic "foo.test" main package; the real target is visited separately
		}

		target := realTarget(p)
		n := getNode(target)
		if hasTestFile(p.GoFiles) {
			n.HasTestFiles = true
		}
		for _, f := range p.GoFiles {
			n.Files = append(n.Files, f)
		}

		for _, imp := range p.Imports {
			if imp.Module == nil || !imp.Module.Main || isTestBinaryMain(imp) {
				continue
			}
			impTarget := realTarget(imp)
			if impTarget == target {
				continue
			}
			n.Imports[impTarget] = true
		}
	})

	for path, n := range g.Nodes {
		n.Files = dedup(n.Files)
		for _, f := range n.Files {
			g.fileToTarget[f] = path
			dir := filepath.Dir(f)
			g.dirToTargets[dir] = appendUnique(g.dirToTargets[dir], path)
		}
	}
	for path, n := range g.Nodes {
		for imp := range n.Imports {
			g.Importers[imp] = appendUnique(g.Importers[imp], path)
		}
	}

	return g, nil
}

// TargetForFile resolves an absolute file path to the import path of the
// Node it belongs to. ok is false if the file isn't part of any known
// package (e.g. it's a non-Go file, or it was deleted and its directory no
// longer resolves to a single unambiguous target).
func (g *Graph) TargetForFile(absPath string) (target string, ok bool) {
	if t, ok := g.fileToTarget[absPath]; ok {
		return t, true
	}
	dir := filepath.Dir(absPath)
	if targets := g.dirToTargets[dir]; len(targets) == 1 {
		return targets[0], true
	}
	return "", false
}

func realTarget(p *packages.Package) string {
	if idx := strings.Index(p.ID, " ["); idx >= 0 {
		bracket := strings.TrimSuffix(p.ID[idx+2:len(p.ID)-1], ".test")
		return bracket
	}
	return p.PkgPath
}

func isTestBinaryMain(p *packages.Package) bool {
	return !strings.Contains(p.ID, " [") && strings.HasSuffix(p.ID, ".test")
}

func hasTestFile(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			return true
		}
	}
	return false
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
