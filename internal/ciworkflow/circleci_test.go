package ciworkflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/ciworkflow"
)

func writeCircleCI(t *testing.T, repoRoot, content string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".circleci")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectCircleCIFiltersBranchesOnlyString(t *testing.T) {
	dir := t.TempDir()
	writeCircleCI(t, dir, `
version: 2.1
workflows:
  build-and-deploy:
    jobs:
      - build
      - deploy:
          requires:
            - build
          filters:
            branches:
              only: main
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want %q (from deploy's filters.branches.only)", got.Branch, "main")
	}
	if got.Reason == "" {
		t.Error("Reason is empty, want an explanation of the CircleCI filters fallback")
	}
}

func TestDetectCircleCIFiltersBranchesOnlyList(t *testing.T) {
	dir := t.TempDir()
	writeCircleCI(t, dir, `
version: 2.1
workflows:
  release:
    jobs:
      - deploy:
          filters:
            branches:
              only:
                - release
                - main
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "release" {
		t.Errorf("Branch = %q, want %q (the first entry in the only: list)", got.Branch, "release")
	}
}

// TestDetectCircleCIIgnoresRegexBranchFilter reproduces a real CircleCI
// feature: a branch filter can be a regex wrapped in slashes (e.g.
// "/release-.*/"), which names a pattern, not one concrete branch - that
// must not be returned as if it were a literal branch name. A second,
// real literal later in the same only: list confirms detection kept
// scanning past the regex rather than just happening to find nothing at
// all (which would "pass" a weaker assertion even if broken outright).
func TestDetectCircleCIIgnoresRegexBranchFilter(t *testing.T) {
	dir := t.TempDir()
	writeCircleCI(t, dir, `
workflows:
  release:
    jobs:
      - deploy:
          filters:
            branches:
              only:
                - /release-.*/
                - main
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "main" {
		t.Errorf("Branch = %q, want %q (the regex entry should be skipped, and the real literal after it still found)", got.Branch, "main")
	}
}

func TestDetectCircleCIBareJobNameHasNoFilters(t *testing.T) {
	dir := t.TempDir()
	writeCircleCI(t, dir, `
workflows:
  build:
    jobs:
      - build
      - test
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch == "build" || got.Branch == "test" {
		t.Errorf("Branch = %q, a bare job name (no filters at all) was mistaken for a branch", got.Branch)
	}
}

// TestDetectPrefersGitLabOverCircleCI checks the documented priority
// order between the two non-GitHub-Actions sources.
func TestDetectPrefersGitLabOverCircleCI(t *testing.T) {
	dir := t.TempDir()
	writeGitLabCI(t, dir, `
deploy:
  script: ./deploy.sh
  only:
    - master
`)
	writeCircleCI(t, dir, `
workflows:
  build:
    jobs:
      - deploy:
          filters:
            branches:
              only: main
`)
	got, err := ciworkflow.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "master" {
		t.Errorf("Branch = %q, want %q (GitLab should take priority over CircleCI)", got.Branch, "master")
	}
}
