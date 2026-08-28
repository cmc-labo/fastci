// Package goanalyzer implements the fastci analyzer.Analyzer interface for
// Go modules and workspaces, keyed by the import path you would pass to
// `go test`.
//
// It is built on top of golang.org/x/tools/go/packages with Tests:true,
// which yields several synthetic package variants per directory (the plain
// production package, an internal test-augmented package, an external
// "_test" package, and a synthetic test-binary main package). This package
// collapses all of those back down into one graph.Node per test target,
// since that's the unit fastci ultimately runs `go test` against.
package goanalyzer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/runner"
)

// Analyzer is the Go implementation of analyzer.Analyzer.
type Analyzer struct{}

// New returns a Go analyzer.
func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) Name() string { return "go" }

// Detect reports whether dir is a Go module root (go.mod present) or a Go
// workspace root (go.work active for dir).
func (*Analyzer) Detect(dir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		return true, nil
	}
	work, err := goEnv(dir, "GOWORK")
	if err != nil {
		return false, err
	}
	return work != "", nil
}

// Build builds the dependency graph for the Go module (or workspace) rooted
// at dir by invoking `go list` (via golang.org/x/tools/go/packages).
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
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
		return nil, fmt.Errorf("goanalyzer: loading packages: %w", err)
	}

	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("goanalyzer: %d package load error(s), e.g. %s", len(loadErrs), loadErrs[0])
	}

	g := graph.New()

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Module == nil || !p.Module.Main {
			return // stdlib or third-party dependency: not part of the module(s) under test
		}
		if isTestBinaryMain(p) {
			return // synthetic "foo.test" main package; the real target is visited separately
		}

		target := realTarget(p)
		n := g.Node(target)
		if hasTestFile(p.GoFiles) {
			n.HasTestFiles = true
		}
		n.Files = append(n.Files, p.GoFiles...)

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

	g.IndexFiles()
	g.BuildImporters()

	return g, nil
}

// FullRunFile reports whether a changed file should force a full test run:
// changes to go.mod/go.sum/go.work/go.work.sum can affect the build list or
// dependency versions in ways the import graph alone doesn't capture.
func (*Analyzer) FullRunFile(absPath string) bool {
	switch filepath.Base(absPath) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	return false
}

// Ignorable reports whether a changed non-Go file is safe to ignore.
func (*Analyzer) Ignorable(absPath string) bool {
	return filepath.Ext(absPath) != ".go"
}

// AllTargets returns the `go list`-style patterns covering every package
// rooted at dir, for a full/--all run.
func (*Analyzer) AllTargets(dir string) ([]string, error) {
	return Patterns(dir)
}

// RunTests runs `go test` against targets (import paths or patterns).
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	argv := append([]string{"go", "test"}, extraArgs...)
	argv = append(argv, targets...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
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
		return nil, fmt.Errorf("goanalyzer: no go.mod in %s, and it is not a Go workspace root (no active go.work)", dir)
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
		return nil, fmt.Errorf("goanalyzer: listing workspace modules: %w%s", err, stderrSuffix(stderr.String()))
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

// stderrSuffix formats captured stderr for appending to an error message:
// "\n<trimmed stderr>" if there's anything to show, or "" if stderr was
// empty (e.g. the command never started) - avoiding a dangling blank line.
func stderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return "\n" + stderr
}

func goEnv(dir, key string) (string, error) {
	cmd := exec.Command("go", "env", key)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("goanalyzer: go env %s: %w%s", key, err, stderrSuffix(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// relPattern builds a `go list`-style "<dir>/..." pattern for target,
// expressed relative to base.
func relPattern(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", fmt.Errorf("goanalyzer: resolving %s relative to %s: %w", target, base, err)
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
