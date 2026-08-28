// Package gitdiff resolves the set of files changed in a git working tree,
// relative to a base commit/ref or the last commit.
package gitdiff

import (
	"bytes"
	"errors"
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
// shows for a pull request against its target branch. A CI checkout is
// commonly shallow (e.g. actions/checkout's default fetch-depth: 1) and/or
// only fetches the ref being built, not the PR's target branch; see
// ensureMergeBaseHistory for how that's recovered from automatically.
func ChangedFiles(repoRoot, base string) ([]string, error) {
	var args []string
	if base == "" {
		args = []string{"diff", "--name-only", "HEAD"}
	} else {
		if err := ensureMergeBaseHistory(repoRoot, base); err != nil {
			return nil, err
		}
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

// ensureMergeBaseHistory makes sure repoRoot's history is deep enough to
// resolve a merge-base against base, automatically fetching more history
// rather than letting the eventual diff fail outright. Two distinct gaps
// are common in CI and both are recovered from:
//
//   - The clone is shallow (e.g. actions/checkout's default
//     fetch-depth: 1), so even if base resolves to a commit, no common
//     ancestor with HEAD is reachable yet: recovered via
//     `git fetch --unshallow`.
//   - base itself doesn't exist locally at all - a checkout action
//     commonly fetches only the ref being built, not the PR's target
//     branch: recovered by fetching it explicitly, mirroring what a user
//     would otherwise add to their CI workflow by hand (see the README).
//
// If base looks like "<remote>/<ref>" (the form the README itself
// recommends, e.g. "origin/main"), the second recovery fetches that ref
// under the exact local name base expects; other forms (a bare branch
// name, a tag, a SHA) skip that step, since there's no reliable remote to
// infer.
func ensureMergeBaseHistory(repoRoot, base string) error {
	if _, err := runGit(repoRoot, "merge-base", base, "HEAD"); err == nil {
		return nil // already resolvable, nothing to do
	}

	var recoveryErrs []string

	if shallow, err := runGit(repoRoot, "rev-parse", "--is-shallow-repository"); err == nil && len(shallow) > 0 && shallow[0] == "true" {
		if _, err := runGit(repoRoot, "fetch", "--unshallow"); err != nil {
			recoveryErrs = append(recoveryErrs, fmt.Sprintf("git fetch --unshallow: %v", err))
		}
	}

	if remote, ref, ok := strings.Cut(base, "/"); ok {
		if _, err := runGit(repoRoot, "fetch", remote, ref+":refs/remotes/"+remote+"/"+ref); err != nil {
			recoveryErrs = append(recoveryErrs, fmt.Sprintf("git fetch %s %s: %v", remote, ref, err))
		}
	}

	if _, err := runGit(repoRoot, "merge-base", base, "HEAD"); err != nil {
		msg := fmt.Sprintf("gitdiff: no merge base found between %q and HEAD, even after fetching more history - this usually means a shallow CI checkout (e.g. actions/checkout's default fetch-depth: 1) that never fetched %q at all: %v", base, base, err)
		if len(recoveryErrs) > 0 {
			msg += "; automatic recovery fetches also failed: " + strings.Join(recoveryErrs, "; ")
		}
		return errors.New(msg)
	}
	return nil
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
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
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
