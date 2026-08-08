// Package gitdiff resolves the set of files changed in a git working tree,
// relative to a base commit/ref or the last commit.
package gitdiff

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ChangedFiles returns the list of files changed, as absolute paths.
//
// If base is empty, it reports the files that differ between HEAD and the
// current working tree (staged + unstaged), which matches a local
// "what have I touched" workflow.
//
// If base is non-empty, it reports the files that differ between the merge
// base of base and HEAD, and HEAD — i.e. the same three-dot diff GitHub
// shows for a pull request against its target branch.
func ChangedFiles(repoRoot, base string) ([]string, error) {
	var args []string
	if base == "" {
		args = []string{"diff", "--name-only", "HEAD"}
	} else {
		args = []string{"diff", "--name-only", base + "...HEAD"}
	}

	out, err := runGit(repoRoot, args...)
	if err != nil {
		return nil, err
	}

	// Untracked files are not covered by `git diff`; a new file that hasn't
	// been staged yet should still count as a change for local runs.
	if base == "" {
		untracked, err := runGit(repoRoot, "ls-files", "--others", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		out = append(out, untracked...)
	}

	seen := make(map[string]bool, len(out))
	files := make([]string, 0, len(out))
	for _, rel := range out {
		rel = strings.TrimSpace(rel)
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		files = append(files, filepath.Join(repoRoot, rel))
	}
	return files, nil
}

// RepoRoot returns the top-level directory of the git repository containing
// dir.
func RepoRoot(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "", fmt.Errorf("gitdiff: could not determine repo root from %q", dir)
	}
	return strings.TrimSpace(out[0]), nil
}

func runGit(dir string, args ...string) ([]string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	lines := strings.Split(stdout.String(), "\n")
	result := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" {
			result = append(result, l)
		}
	}
	return result, nil
}
