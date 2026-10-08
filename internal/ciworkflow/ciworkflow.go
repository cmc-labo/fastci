// Package ciworkflow figures out what base branch CI would diff a pull
// request against, so that `fastci local` can reproduce the same impact
// analysis locally instead of guessing (or requiring the user to pass
// --base by hand). GitHub Actions workflow files get the most precise
// treatment, since their schema actually declares a pull request's target
// branch; GitLab CI/CD and CircleCI configs get a best-effort reading of
// whatever branch their own push/deploy triggers already name (see
// gitlab.go/circleci.go for exactly why that's the best available signal
// for either); and FASTCI_BASE_BRANCH is a universal override that works
// identically regardless of CI platform - including ones this package has
// no dedicated support for at all (Bitbucket Pipelines, Azure DevOps,
// Travis, Buildkite, ...).
package ciworkflow

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Detection is the result of inspecting a repository for its CI base
// branch. Reason is a short, human-readable explanation of where Branch
// came from, meant to be printed so the user can see why fastci chose it.
type Detection struct {
	Branch string
	Reason string
}

// workflowFile is deliberately a partial, permissive mapping of a GitHub
// Actions workflow: fastci only needs the "on.pull_request.branches" and
// "on.push.branches" triggers, not the rest of the schema (jobs, steps,
// permissions, etc.), so everything else is left unparsed.
type workflowFile struct {
	On onBlock `yaml:"on"`
}

// onBlock's fields are pointers so a workflow that omits "pull_request" (or
// gives it a bare, filter-less trigger, which YAML represents as a null
// value) can be told apart from one that supplies an explicit, empty
// branches list.
type onBlock struct {
	PullRequest *branchFilter `yaml:"pull_request"`
	Push        *branchFilter `yaml:"push"`
}

type branchFilter struct {
	Branches []string `yaml:"branches"`
}

// baseBranchEnvVar is a universal, explicit override for Detect's own
// guesswork: unlike every source below it, it works identically no
// matter which CI platform (or none at all) is in play, and is the only
// way to get a reliable answer on a platform this package has no
// dedicated parser for.
const baseBranchEnvVar = "FASTCI_BASE_BRANCH"

// Detect figures out the base branch fastci should diff against to match
// CI, trying each of the following in order and returning the first
// match:
//
//  0. The FASTCI_BASE_BRANCH environment variable, if set - see
//     baseBranchEnvVar.
//  1. The first explicit "on.pull_request.branches" entry found in any
//     GitHub Actions workflow file under .github/workflows/*.yml (or
//     *.yaml), sorted by filename for determinism across multiple
//     matching workflows.
//  2. Failing that, the first explicit "on.push.branches" entry in any
//     such workflow - a repo's main integration branch is almost always
//     both what pushes deploy from and what pull requests target, and a
//     bare "pull_request:" trigger (no branches filter at all) is
//     extremely common precisely because GitHub already restricts it to
//     the PR's own base branch, so the workflow file itself often has no
//     branch name to read at all.
//  3. Failing that, a branch literal named by a job's own rules/only
//     trigger in a GitLab CI/CD .gitlab-ci.yml - see gitlab.go for why
//     this is the best signal GitLab's own config format can give.
//  4. Failing that, a branch literal named by a job's own
//     filters.branches.only in a CircleCI .circleci/config.yml - see
//     circleci.go for the same reasoning applied there.
//  5. Failing all of the above (no recognized CI config, or none mention
//     a branch anywhere), the repository's actual default branch per
//     "git symbolic-ref refs/remotes/origin/HEAD" - this step alone is
//     genuinely platform-agnostic, unlike 1-4 above.
//  6. As a last resort, the literal string "main".
func Detect(repoRoot string) (Detection, error) {
	if branch := strings.TrimSpace(os.Getenv(baseBranchEnvVar)); branch != "" {
		return Detection{
			Branch: branch,
			Reason: fmt.Sprintf("%s environment variable", baseBranchEnvVar),
		}, nil
	}

	files, err := workflowFiles(repoRoot)
	if err != nil {
		return Detection{}, err
	}

	for _, f := range files {
		wf, err := parseWorkflow(f)
		if err != nil {
			// A workflow file that doesn't parse as YAML (or isn't
			// actually a workflow) shouldn't block detection - just skip
			// it and keep looking at the others.
			continue
		}
		if wf.On.PullRequest != nil && len(wf.On.PullRequest.Branches) > 0 {
			return Detection{
				Branch: wf.On.PullRequest.Branches[0],
				Reason: fmt.Sprintf("on.pull_request.branches in %s", relPath(repoRoot, f)),
			}, nil
		}
	}

	for _, f := range files {
		wf, err := parseWorkflow(f)
		if err != nil {
			continue
		}
		if wf.On.Push != nil && len(wf.On.Push.Branches) > 0 {
			return Detection{
				Branch: wf.On.Push.Branches[0],
				Reason: fmt.Sprintf("no pull_request.branches filter found, inferred from on.push.branches in %s", relPath(repoRoot, f)),
			}, nil
		}
	}

	if branch, ok := detectGitLab(repoRoot); ok {
		return Detection{
			Branch: branch,
			Reason: fmt.Sprintf("no GitHub Actions workflow specifies a branch, inferred from a rules/only trigger in %s", gitlabCIFile),
		}, nil
	}

	if branch, ok := detectCircleCI(repoRoot); ok {
		return Detection{
			Branch: branch,
			Reason: fmt.Sprintf("no GitHub Actions or GitLab CI/CD config specifies a branch, inferred from a filters.branches.only trigger in %s", circleCIFile),
		}, nil
	}

	if branch, ok := defaultRemoteBranch(repoRoot); ok {
		return Detection{
			Branch: branch,
			Reason: "no recognized CI config specifies a branch, using the repository's default branch (origin/HEAD)",
		}, nil
	}

	return Detection{
		Branch: "main",
		Reason: `no recognized CI config specifies a branch and the repository's default branch could not be determined, falling back to "main"`,
	}, nil
}

// workflowFiles returns every *.yml/*.yaml file directly under
// .github/workflows, sorted by name for deterministic results. A missing
// directory is not an error - most repositories fastci runs against won't
// have one, and Detect should fall through to its other strategies.
func workflowFiles(repoRoot string) ([]string, error) {
	dir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".yml" || ext == ".yaml" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files, nil
}

func parseWorkflow(path string) (workflowFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return workflowFile{}, err
	}
	var wf workflowFile
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return workflowFile{}, err
	}
	return wf, nil
}

func relPath(repoRoot, path string) string {
	if rel, err := filepath.Rel(repoRoot, path); err == nil {
		return rel
	}
	return path
}

func defaultRemoteBranch(repoRoot string) (string, bool) {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	// Output looks like "refs/remotes/origin/main\n".
	ref := strings.TrimSpace(string(out))
	const prefix = "refs/remotes/origin/"
	if !strings.HasPrefix(ref, prefix) {
		return "", false
	}
	branch := strings.TrimPrefix(ref, prefix)
	if branch == "" {
		return "", false
	}
	return branch, true
}
