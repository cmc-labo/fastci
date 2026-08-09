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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// Graph is the full dependency graph for a module (or, in workspace mode,
// every module listed in go.work).
type Graph struct {
	// ModulePaths is the sorted set of main module paths the graph was
	// built from - one entry for a plain module, several in workspace mode.
	ModulePaths []string
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

// Load builds the dependency graph for the Go module (or workspace) rooted
// at dir by invoking `go list` (via golang.org/x/tools/go/packages).
func Load(dir string) (*Graph, error) {
	patterns, err := Patterns(dir)
	if err != nil {
		return nil, err
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedModule,
		Dir:   dir,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, patterns...)
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
			return // stdlib or third-party dependency: not part of the module(s) under test
		}
		g.ModulePaths = appendUnique(g.ModulePaths, p.Module.Path)
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
	sort.Strings(g.ModulePaths)

	return g, nil
}

// Patterns returns the `go list`-style package patterns that cover every
// package rooted at dir: "./..." for a plain module, or one "<reldir>/..."
// pattern per member module when dir is a workspace root (a directory with
// a go.work but no go.mod of its own).
func Patterns(dir string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return []string{"./..."}, nil
	}

	moduleDirs, err := workspaceModuleDirs(dir)
	if err != nil {
		return nil, err
	}
	if len(moduleDirs) == 0 {
		return nil, fmt.Errorf("depgraph: no go.mod in %s, and it is not a Go workspace root (no active go.work)", dir)
	}

	patterns := make([]string, 0, len(moduleDirs))
	for _, mdir := range moduleDirs {
		pattern, err := relPattern(dir, mdir)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// workspaceModuleDirs returns the directories of every module in scope for
// the workspace containing dir (empty, with no error, if dir isn't in
// workspace mode at all - e.g. GOWORK=off or no go.work above it).
func workspaceModuleDirs(dir string) ([]string, error) {
	work, err := goEnv(dir, "GOWORK")
	if err != nil {
		return nil, err
	}
	if work == "" {
		return nil, nil
	}

	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("depgraph: listing workspace modules: %w\n%s", err, stderr.String())
	}

	var dirs []string
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			dirs = append(dirs, line)
		}
	}
	return dirs, nil
}

func goEnv(dir, key string) (string, error) {
	cmd := exec.Command("go", "env", key)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("depgraph: go env %s: %w\n%s", key, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// relPattern builds a `go list`-style "<dir>/..." pattern for target,
// expressed relative to base.
func relPattern(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("depgraph: resolving %s relative to %s: %w", target, base, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "./...", nil
	}
	if !strings.HasPrefix(rel, "..") {
		rel = "./" + rel
	}
	return rel + "/...", nil
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
