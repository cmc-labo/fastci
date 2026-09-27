package guard_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/guard"
)

func TestCargoBuildScriptsDetect(t *testing.T) {
	dir := t.TempDir()
	ok, err := guard.CargoBuildScripts{}.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Detect = true on an empty directory, want false")
	}

	writeFile(t, filepath.Join(dir, "Cargo.toml"), "")
	ok, err = guard.CargoBuildScripts{}.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Detect = false after creating Cargo.toml, want true")
	}
}

// setupCargoProjectWithPathDep writes a minimal "app" crate - itself
// given a build script too, specifically so a test can verify it's
// correctly excluded as the workspace's own root member, not just
// happening to lack one - depending on a local path dependency, optionally
// with its own custom build script - entirely offline (no crates.io fetch
// needed): a plain path dependency that isn't itself listed under the
// root crate's [workspace] members (or, as here, isn't in any [workspace]
// at all) is resolved by `cargo metadata` as a real, distinct,
// non-workspace-member package, the exact same shape a genuine
// third-party registry dependency would have.
func setupCargoProjectWithPathDep(t *testing.T, hasBuildScript bool) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.toml"), `[package]
name = "app"
version = "0.1.0"
edition = "2021"
build = "build.rs"

[dependencies]
dep = { path = "dep" }
`)
	writeFile(t, filepath.Join(dir, "build.rs"), "fn main() {}\n")
	writeFile(t, filepath.Join(dir, "src", "main.rs"), "fn main() {}\n")

	depManifest := `[package]
name = "dep"
version = "0.1.0"
edition = "2021"
`
	if hasBuildScript {
		depManifest += "build = \"build.rs\"\n"
		writeFile(t, filepath.Join(dir, "dep", "build.rs"), "fn main() {}\n")
	}
	writeFile(t, filepath.Join(dir, "dep", "Cargo.toml"), depManifest)
	writeFile(t, filepath.Join(dir, "dep", "src", "lib.rs"), "pub fn hello() {}\n")
	return dir
}

func TestCargoBuildScriptsFindsDependencyBuildScript(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed - skipping this real integration test")
	}
	dir := setupCargoProjectWithPathDep(t, true)

	res, err := guard.CargoBuildScripts{}.Run(context.Background(), dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true; output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "dep@0.1.0") || !strings.Contains(res.Output, "build.rs") {
		t.Errorf("output missing the dependency's build script:\n%s", res.Output)
	}
	// The root crate "app" itself is never flagged - only dependencies are
	// in scope, the same as LifecycleScripts only scanning node_modules.
	if strings.Contains(res.Output, "app@") {
		t.Errorf("output mentions the root crate \"app\", which should never be flagged:\n%s", res.Output)
	}
}

func TestCargoBuildScriptsCleanWhenNoDependencyHasOne(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed - skipping this real integration test")
	}
	dir := setupCargoProjectWithPathDep(t, false)

	res, err := guard.CargoBuildScripts{}.Run(context.Background(), dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FoundIssues {
		t.Errorf("FoundIssues = true, want false; output:\n%s", res.Output)
	}
}
