package ciworkflow_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/ciworkflow"
)

func writeWorkflow(t *testing.T, repoRoot, name, content string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectExplicitPullRequestBranches(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
on:
  pull_request:
    branches: [develop]
  push:
    branches: [main]
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "develop" {
		t.Errorf("Branch = %q, want %q", got.Branch, "develop")
	}
}

// TestDetectFallsBackToPushBranches reproduces fastci's own real workflow
// file: a bare "pull_request:" trigger with no branches filter at all
// (extremely common, since GitHub already scopes the trigger to the PR's
// own base branch) alongside an explicit "push.branches". Detect should
// fall back to the push branch rather than reporting nothing.
func TestDetectFallsBackToPushBranches(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
name: CI

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: go test ./...
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want %q (from push.branches)", got.Branch, "main")
	}
	if got.Reason == "" {
		t.Error("Reason is empty, want an explanation of the push.branches fallback")
	}
}

func TestDetectNoWorkflowsFallsBackToGitDefaultBranch(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "trunk")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-q", "-m", "init")

	// No real remote is configured, so refs/remotes/origin/HEAD won't
	// resolve either - this should hit the final hardcoded "main" fallback,
	// not error out.
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want the hardcoded fallback %q", got.Branch, "main")
	}
}

func TestDetectUsesOriginHEADWhenNoWorkflowMentionsABranch(t *testing.T) {
	remote := t.TempDir()
	runGit(t, remote, "init", "-q", "-b", "trunk")
	runGit(t, remote, "config", "user.email", "test@example.com")
	runGit(t, remote, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(remote, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remote, "add", "f.txt")
	runGit(t, remote, "commit", "-q", "-m", "init")

	local := t.TempDir()
	runGit(t, local, "clone", "-q", remote, ".")

	got, err := ciworkflow.Detect(local)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "trunk" {
		t.Errorf("Branch = %q, want %q (from origin/HEAD)", got.Branch, "trunk")
	}
}

func TestDetectIgnoresUnparseableWorkflowFile(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "broken.yml", "this: [is not, valid yaml")
	writeWorkflow(t, dir, "zz-good.yml", `
on:
  pull_request:
    branches: [release]
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "release" {
		t.Errorf("Branch = %q, want %q (the broken file should be skipped, not fatal)", got.Branch, "release")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
