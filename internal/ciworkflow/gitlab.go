package ciworkflow

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"go.yaml.in/yaml/v3"
)

// gitlabCIFile is the canonical GitLab CI/CD config filename at a
// repository's root. Unlike GitHub Actions, GitLab doesn't declare a
// merge request's target branch anywhere in this file at all - it's
// decided per-MR on GitLab itself and only available at pipeline runtime
// via $CI_MERGE_REQUEST_TARGET_BRANCH_NAME, an environment variable that
// doesn't exist on a developer's local machine (fastci local's whole
// reason to exist). The next best, genuinely available signal is a
// branch name a job's own rules/only already names for its push/deploy
// trigger - the same "a repo's main integration branch is almost always
// also what pull/merge requests target" reasoning the GitHub Actions
// on.push.branches fallback already relies on.
const gitlabCIFile = ".gitlab-ci.yml"

// detectGitLab looks for the first branch literal any top-level entry in
// <repoRoot>/.gitlab-ci.yml names in its own rules/only trigger - e.g.
// `rules: - if: '$CI_COMMIT_BRANCH == "main"'` (checked against both job
// entries and the pipeline-wide workflow: block, which has the identical
// shape) or the legacy `only: [main]` - and returns it, sorted by
// top-level key name for determinism when more than one entry names a
// branch. ok is false if the file doesn't exist, isn't valid YAML, or
// names no branch this way at all - common, since many real
// .gitlab-ci.yml files gate on $CI_DEFAULT_BRANCH, a variable reference
// rather than a literal, which carries no new information over what
// Detect's own origin/HEAD fallback already provides.
//
// This only reads the top-level file - job templates split across other
// files via GitLab's own `include:` are not followed, the same kind of
// documented scope limit already noted for a bare "pull_request:" GitHub
// Actions trigger with no filter of its own to read.
func detectGitLab(repoRoot string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(repoRoot, gitlabCIFile))
	if err != nil {
		return "", false
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", false
	}

	names := make([]string, 0, len(doc))
	for name := range doc {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		entry, ok := doc[name].(map[string]interface{})
		if !ok {
			continue
		}
		if b, ok := branchFromGitLabRules(entry["rules"]); ok {
			return b, true
		}
		if b, ok := branchFromGitLabOnly(entry["only"]); ok {
			return b, true
		}
	}
	return "", false
}

// gitlabBranchRuleVar matches a `rules[].if` condition comparing one of
// GitLab's own predefined branch-name variables against a literal, e.g.
// `$CI_COMMIT_BRANCH == "main"` or `$CI_COMMIT_REF_NAME == 'main'`. It
// deliberately never matches a comparison against another variable (e.g.
// $CI_DEFAULT_BRANCH) - only a quoted literal is a genuinely new branch
// name, not something origin/HEAD could already tell Detect anyway.
var gitlabBranchRuleVar = regexp.MustCompile(`\$CI_COMMIT_(?:BRANCH|REF_NAME)\s*==\s*["']([^"']+)["']`)

func branchFromGitLabRules(rules interface{}) (string, bool) {
	list, ok := rules.([]interface{})
	if !ok {
		return "", false
	}
	for _, r := range list {
		rule, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		cond, ok := rule["if"].(string)
		if !ok {
			continue
		}
		if m := gitlabBranchRuleVar.FindStringSubmatch(cond); m != nil {
			return m[1], true
		}
	}
	return "", false
}

// branchFromGitLabOnly reads GitLab's legacy `only:` job key, in either
// its plain list form (`only: [main]`) or its map form (`only: {refs:
// [main]}`) - GitLab's own docs now recommend `rules:` instead, but
// plenty of real configs still use this.
func branchFromGitLabOnly(only interface{}) (string, bool) {
	switch v := only.(type) {
	case []interface{}:
		return firstNonEmptyString(v)
	case map[string]interface{}:
		refs, _ := v["refs"].([]interface{})
		return firstNonEmptyString(refs)
	default:
		return "", false
	}
}

func firstNonEmptyString(items []interface{}) (string, bool) {
	for _, it := range items {
		if s, ok := it.(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}
