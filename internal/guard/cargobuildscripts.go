package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// CargoBuildScripts flags dependency crates (direct or transitive) that
// define a custom build script (conventionally build.rs, though
// Cargo.toml's own `build = "..."` field can point anywhere) - the same
// class of supply-chain risk as npm's install-time lifecycle scripts (see
// LifecycleScripts): a build script runs arbitrary Rust code automatically
// at build time, before the crate's own code is even compiled or used,
// and real malicious-crate incidents have used exactly this mechanism.
//
// Like LifecycleScripts, this doesn't judge intent - linking a C library,
// generating code from a schema, or emitting cfg flags for conditional
// compilation are all common, legitimate uses of a build script - it just
// surfaces which dependencies can run code at build time, so a human can
// look.
//
// Unlike a filename guess, detection here is exact: `cargo metadata`
// reports each package's own targets, and a build script always shows up
// with target kind "custom-build" - Cargo's own resolution of whatever
// Cargo.toml's `build` field actually says, not an assumption that the
// file is named build.rs.
type CargoBuildScripts struct{}

func (CargoBuildScripts) Name() string { return "cargo build scripts" }

func (CargoBuildScripts) Detect(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "Cargo.toml"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (CargoBuildScripts) BinaryAvailable(dir string) (bool, string) {
	if _, err := exec.LookPath("cargo"); err == nil {
		return true, ""
	}
	return false, "cargo not found on PATH (install: https://rustup.rs)"
}

type cargoMetadataTargets struct {
	Packages []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version string `json:"version"`
		Targets []struct {
			Kind    []string `json:"kind"`
			SrcPath string   `json:"src_path"`
		} `json:"targets"`
	} `json:"packages"`
	// WorkspaceMembers holds full package IDs (e.g.
	// "path+file:///repo#mycrate@0.1.0"), not plain crate names - a crate
	// name alone isn't even guaranteed unique across a dependency graph
	// (different major versions of the same crate can coexist), so this
	// must be compared against Packages[].ID, never Packages[].Name.
	WorkspaceMembers []string `json:"workspace_members"`
}

func (CargoBuildScripts) Run(ctx context.Context, dir string) (Result, error) {
	// Unlike every other Checker's Run, this one needs to actually parse
	// structured output rather than just display it - `cargo metadata`'s
	// stdout *is* the JSON to parse, not a human report - so stdout and
	// stderr have to be captured separately: `cargo metadata` routinely
	// writes real, non-JSON progress text to stderr (e.g. "Updating
	// crates.io index" the first time a new dependency is resolved), and
	// combining the two streams (as the shared runCmd helper deliberately
	// does for every other checker, which only ever displays output)
	// would corrupt the JSON with that interleaved text.
	cmd := exec.CommandContext(ctx, "cargo", "metadata", "--format-version=1")
	cmd.Dir = dir
	isolateProcessGroup(cmd)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return Result{}, fmt.Errorf("cargo metadata: %w\n%s", err, exitErr.Stderr)
		}
		return Result{}, fmt.Errorf("cargo metadata: %w", err)
	}

	var meta cargoMetadataTargets
	if err := json.Unmarshal(stdout, &meta); err != nil {
		return Result{}, fmt.Errorf("cargo build scripts: parsing cargo metadata output: %w", err)
	}

	// Only dependencies are in scope, the same as LifecycleScripts only
	// scans node_modules - a project's own build.rs is something its own
	// developers already wrote and can already see, not a third-party
	// supply-chain risk.
	isWorkspaceMember := make(map[string]bool, len(meta.WorkspaceMembers))
	for _, id := range meta.WorkspaceMembers {
		isWorkspaceMember[id] = true
	}

	type hit struct{ name, version, path string }
	var hits []hit
	for _, p := range meta.Packages {
		if isWorkspaceMember[p.ID] {
			continue
		}
		for _, t := range p.Targets {
			for _, k := range t.Kind {
				if k == "custom-build" {
					hits = append(hits, hit{name: p.Name, version: p.Version, path: t.SrcPath})
				}
			}
		}
	}

	res := Result{CheckerName: "cargo build scripts"}
	if len(hits) == 0 {
		res.Output = "no dependency defines a custom build script"
		return res, nil
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].name != hits[j].name {
			return hits[i].name < hits[j].name
		}
		return hits[i].version < hits[j].version
	})

	res.FoundIssues = true
	noun, verb := "dependency", "defines"
	if len(hits) != 1 {
		noun, verb = "dependencies", "define"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s %s a custom build script - not necessarily malicious, but each one runs arbitrary code automatically at build time, so review any you don't recognize:\n", len(hits), noun, verb)
	for _, h := range hits {
		fmt.Fprintf(&b, "  %s@%s: %s\n", h.name, h.version, h.path)
	}
	res.Output = strings.TrimRight(b.String(), "\n")
	return res, nil
}
