package ciworkflow

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// circleCIFile is the canonical CircleCI config path at a repository's
// root. Like GitLab, CircleCI's own config schema has no concept of a
// pull request's target branch at all - PR awareness comes entirely from
// its VCS integration (GitHub/Bitbucket), never from anything declared in
// this file - so the best available signal is a branch a job already
// names for its own push trigger, not a merge-request-specific one.
const circleCIFile = ".circleci/config.yml"

// detectCircleCI looks for the first branch name any job in
// <repoRoot>/.circleci/config.yml restricts itself to via its
// filters.branches.only, across every workflow (sorted by workflow name,
// then job order within it, for determinism), and returns it. ok is
// false if the file doesn't exist, isn't valid YAML, or no job filters on
// a concrete branch name this way.
func detectCircleCI(repoRoot string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(repoRoot, circleCIFile))
	if err != nil {
		return "", false
	}
	var doc struct {
		Workflows map[string]interface{} `yaml:"workflows"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return "", false
	}

	names := make([]string, 0, len(doc.Workflows))
	for name := range doc.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		// A plain top-level "version: 2" key under workflows: (older
		// schema) isn't itself a workflow - it fails this type assertion
		// and is harmlessly skipped, same as any other non-workflow entry.
		wf, ok := doc.Workflows[name].(map[string]interface{})
		if !ok {
			continue
		}
		jobs, ok := wf["jobs"].([]interface{})
		if !ok {
			continue
		}
		for _, j := range jobs {
			// Each entry is either a bare job name (a string, meaning no
			// filters at all) or a single-key map {jobName: {filters:
			// ...}} - only the latter shape can name a branch.
			jobCfg, ok := j.(map[string]interface{})
			if !ok {
				continue
			}
			for _, cfg := range jobCfg {
				cfgMap, ok := cfg.(map[string]interface{})
				if !ok {
					continue
				}
				if b, ok := branchFromCircleCIFilters(cfgMap["filters"]); ok {
					return b, true
				}
			}
		}
	}
	return "", false
}

func branchFromCircleCIFilters(filters interface{}) (string, bool) {
	f, ok := filters.(map[string]interface{})
	if !ok {
		return "", false
	}
	branches, ok := f["branches"].(map[string]interface{})
	if !ok {
		return "", false
	}
	switch only := branches["only"].(type) {
	case string:
		if isLiteralBranchName(only) {
			return only, true
		}
	case []interface{}:
		return firstLiteralBranchName(only)
	}
	return "", false
}

func firstLiteralBranchName(items []interface{}) (string, bool) {
	for _, it := range items {
		if s, ok := it.(string); ok && isLiteralBranchName(s) {
			return s, true
		}
	}
	return "", false
}

// isLiteralBranchName reports whether s looks like a plain branch name
// rather than a CircleCI regex filter (wrapped in slashes, e.g.
// "/release-.*/"), which names a pattern, not one concrete branch Detect
// could diff against.
func isLiteralBranchName(s string) bool {
	return s != "" && !strings.HasPrefix(s, "/")
}
