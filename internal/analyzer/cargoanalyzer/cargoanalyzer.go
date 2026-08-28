// Package cargoanalyzer implements the fastci analyzer.Analyzer interface
// for Rust crates and Cargo workspaces, at crate (package) granularity -
// the same level `cargo test -p <crate>` operates at, and analogous to
// goanalyzer's package granularity.
//
// The dependency graph comes from `cargo metadata`, which resolves the
// real dependency graph (path deps, normal/dev/build dependencies, feature
// resolution) exactly the way Cargo itself would - the same
// "delegate to the real toolchain" approach as go/packages for Go.
package cargoanalyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/runner"
)

// Analyzer is the Cargo implementation of analyzer.Analyzer.
type Analyzer struct{}

// New returns a Cargo analyzer.
func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) Name() string { return "cargo" }

// Detect reports whether dir is a Cargo crate or workspace root (a
// Cargo.toml present). Cargo itself walks upward to find the enclosing
// workspace root regardless of which member's directory it's invoked
// from, so a Cargo.toml belonging to a workspace member is just as valid a
// signal as the workspace root's own.
func (*Analyzer) Detect(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "Cargo.toml"))
	return err == nil, nil
}

type cargoMetadata struct {
	WorkspaceRoot string `json:"workspace_root"`
	Packages      []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		ManifestPath string `json:"manifest_path"`
	} `json:"packages"`
	WorkspaceMembers []string `json:"workspace_members"`
	Resolve          struct {
		Nodes []struct {
			ID   string `json:"id"`
			Deps []struct {
				Name string `json:"name"`
				Pkg  string `json:"pkg"`
			} `json:"deps"`
		} `json:"nodes"`
	} `json:"resolve"`
}

// Build runs `cargo metadata` for the workspace containing dir and turns
// the resolved crate graph into a graph.Graph, one Node per workspace
// member crate. All non-crate files under the workspace (i.e. everything
// but target/ and dotfiles) are attributed to whichever crate's directory
// most specifically contains them.
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
	meta, err := loadMetadata(dir)
	if err != nil {
		return nil, err
	}

	members := make(map[string]bool, len(meta.WorkspaceMembers))
	for _, id := range meta.WorkspaceMembers {
		members[id] = true
	}

	idToName := make(map[string]string, len(meta.Packages))
	type crate struct {
		name string
		dir  string
	}
	var crates []crate
	for _, p := range meta.Packages {
		if !members[p.ID] {
			continue
		}
		idToName[p.ID] = p.Name
		crates = append(crates, crate{name: p.Name, dir: filepath.Dir(p.ManifestPath)})
	}

	g := graph.New()
	for _, c := range crates {
		g.Node(c.name)
	}

	for _, n := range meta.Resolve.Nodes {
		if !members[n.ID] {
			continue
		}
		from, ok := idToName[n.ID]
		if !ok {
			continue
		}
		node := g.Node(from)
		for _, dep := range n.Deps {
			if !members[dep.Pkg] {
				continue // external crate, not part of the workspace
			}
			to, ok := idToName[dep.Pkg]
			if !ok || to == from {
				continue
			}
			node.Imports[to] = true
		}
	}

	// Longest-directory-prefix-wins, same technique as goanalyzer's
	// workspace patterns and pytestanalyzer's source roots, so nested
	// crate directories (rare, but valid) are attributed correctly.
	sort.Slice(crates, func(i, j int) bool { return len(crates[i].dir) > len(crates[j].dir) })

	crateForFile := func(path string) string {
		for _, c := range crates {
			if path == c.dir || strings.HasPrefix(path, c.dir+string(filepath.Separator)) {
				return c.name
			}
		}
		return ""
	}

	skipDir := func(path string) bool {
		base := filepath.Base(path)
		return base == "target" || base == ".git" || (meta.WorkspaceRoot != "" && path == filepath.Join(meta.WorkspaceRoot, "target"))
	}

	err = filepath.WalkDir(meta.WorkspaceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != meta.WorkspaceRoot && skipDir(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".rs" {
			return nil
		}
		name := crateForFile(path)
		if name == "" {
			return nil // .rs file outside every known crate directory
		}
		n := g.Node(name)
		n.Files = append(n.Files, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cargoanalyzer: walking %s: %w", meta.WorkspaceRoot, err)
	}

	for _, c := range crates {
		n := g.Nodes[c.name]
		n.HasTestFiles = crateHasTests(c.dir, n.Files)
	}

	g.IndexFiles()
	g.BuildImporters()
	return g, nil
}

func loadMetadata(dir string) (*cargoMetadata, error) {
	cmd := exec.Command("cargo", "metadata", "--format-version=1")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
				return nil, fmt.Errorf("cargoanalyzer: cargo metadata: %w\n%s", err, stderr)
			}
		}
		return nil, fmt.Errorf("cargoanalyzer: cargo metadata: %w", err)
	}
	var meta cargoMetadata
	if err := json.Unmarshal(out, &meta); err != nil {
		return nil, fmt.Errorf("cargoanalyzer: parsing cargo metadata output: %w", err)
	}
	return &meta, nil
}

var testAttrRE = regexp.MustCompile(`#\s*\[\s*test\s*(\(|\])`)

// crateHasTests reports whether cargo test would find anything to run for
// this crate: an integration test directory, or a `#[test]` attribute
// anywhere in its source. This is a lightweight heuristic (a substring
// scan, not a real parse) purely to keep target counts/reporting
// meaningful - it never gates correctness, since an affected crate with no
// tests is simply a harmless no-op for `cargo test -p`.
func crateHasTests(crateDir string, files []string) bool {
	if info, err := os.Stat(filepath.Join(crateDir, "tests")); err == nil && info.IsDir() {
		return true
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if testAttrRE.Match(data) {
			return true
		}
	}
	return false
}

var fullRunBasenames = map[string]bool{
	"Cargo.toml": true, "Cargo.lock": true, "build.rs": true,
	"rust-toolchain": true, "rust-toolchain.toml": true,
}

// FullRunFile reports whether a changed file should force a full test run:
// any crate's manifest, the lockfile, a build script, or the toolchain
// pin can all change compiled behavior in ways the dependency graph alone
// doesn't capture.
func (*Analyzer) FullRunFile(absPath string) bool {
	if fullRunBasenames[filepath.Base(absPath)] {
		return true
	}
	// .cargo/config.toml (or the legacy .cargo/config) affects the whole
	// workspace's build flags/profiles.
	if filepath.Base(filepath.Dir(absPath)) == ".cargo" {
		base := filepath.Base(absPath)
		return base == "config.toml" || base == "config"
	}
	return false
}

// Ignorable reports whether a changed non-Rust file is safe to ignore.
func (*Analyzer) Ignorable(absPath string) bool {
	return filepath.Ext(absPath) != ".rs"
}

// AllTargets returns nil: `cargo test` run with no -p flags already tests
// the whole workspace, so a full/--all run needs no explicit target list.
func (*Analyzer) AllTargets(dir string) ([]string, error) {
	return nil, nil
}

// RunTests runs `cargo test`, selecting crates with one -p flag per
// target when targets is non-empty.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	argv := []string{"cargo", "test"}
	for _, t := range targets {
		argv = append(argv, "-p", t)
	}
	argv = append(argv, extraArgs...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
}
