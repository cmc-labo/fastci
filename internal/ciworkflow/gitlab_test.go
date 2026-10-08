package ciworkflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/ciworkflow"
)

func writeGitLabCI(t *testing.T, repoRoot, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoRoot, ".gitlab-ci.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDetectGitLabRulesIfBranch covers the modern `rules:` syntax GitLab
// itself now recommends over `only`/`except`.
func TestDetectGitLabRulesIfBranch(t *testing.T) {
	dir := t.TempDir()
	writeGitLabCI(t, dir, `
stages:
  - test
  - deploy

test:
  stage: test
  script: go test ./...

deploy:
  stage: deploy
  script: ./deploy.sh
  rules:
    - if: '$CI_COMMIT_BRANCH == "main"'
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want %q (from deploy's rules.if)", got.Branch, "main")
	}
	if got.Reason == "" {
		t.Error("Reason is empty, want an explanation of the GitLab rules fallback")
	}
}

// TestDetectGitLabLegacyOnlyBranch covers the older `only:` job key,
// still common in real .gitlab-ci.yml files despite GitLab's docs now
// recommending rules: instead.
func TestDetectGitLabLegacyOnlyBranch(t *testing.T) {
	dir := t.TempDir()
	writeGitLabCI(t, dir, `
deploy:
  script: ./deploy.sh
  only:
    - master
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "master" {
		t.Errorf("Branch = %q, want %q (from deploy's legacy only:)", got.Branch, "master")
	}
}

// TestDetectGitLabOnlyRefsMapForm covers only's alternate map form,
// `only: {refs: [...]}`, as opposed to the plain list form.
func TestDetectGitLabOnlyRefsMapForm(t *testing.T) {
	dir := t.TempDir()
	writeGitLabCI(t, dir, `
deploy:
  script: ./deploy.sh
  only:
    refs:
      - staging
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "staging" {
		t.Errorf("Branch = %q, want %q (from only.refs)", got.Branch, "staging")
	}
}

// TestDetectGitLabIgnoresDefaultBranchVariable reproduces a common real
// case: a rule gated on $CI_DEFAULT_BRANCH (a variable, not a literal
// branch name) carries no information Detect's own origin/HEAD fallback
// doesn't already have, so it must not be mistaken for a literal branch
// name. Unlike just checking the bad value is absent (which would also
// "pass" if GitLab detection were broken outright and fell through to
// origin/HEAD regardless), this plants a second, later job with a real
// literal - confirming Detect actually kept scanning past the
// unmatched rule and still found that real signal, not that it merely
// failed to find the wrong one.
func TestDetectGitLabIgnoresDefaultBranchVariable(t *testing.T) {
	dir := t.TempDir()
	writeGitLabCI(t, dir, `
deploy:
  script: ./deploy.sh
  rules:
    - if: '$CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH'

release:
  script: ./release.sh
  rules:
    - if: '$CI_COMMIT_BRANCH == "release"'
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "release" {
		t.Errorf("Branch = %q, want %q (the variable-gated rule should be skipped, not just absent-mindedly matched; the real literal in the next job should still be found)", got.Branch, "release")
	}
}

// TestDetectPrefersGitHubActionsOverGitLab checks the documented priority
// order: a repo migrating between CI platforms (or just keeping stale
// config around) might have both files - GitHub Actions' own, more
// precise pull_request.branches should still win.
func TestDetectPrefersGitHubActionsOverGitLab(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "ci.yml", `
on:
  pull_request:
    branches: [develop]
`)
	writeGitLabCI(t, dir, `
deploy:
  script: ./deploy.sh
  only:
    - master
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "develop" {
		t.Errorf("Branch = %q, want %q (GitHub Actions should take priority over GitLab)", got.Branch, "develop")
	}
}
