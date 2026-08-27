package gitdiff_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hpscript/fastci/internal/gitdiff"
)

// runGitT runs git in dir, failing the test on error. Local clones ignore
// --depth unless the source is addressed via a file:// URL rather than a
// plain path, so tests that need a genuinely shallow clone must pass one.
func runGitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitEnv(t *testing.T) []string {
	t.Helper()
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=fastci-test", "GIT_AUTHOR_EMAIL=fastci-test@example.com",
		"GIT_COMMITTER_NAME=fastci-test", "GIT_COMMITTER_EMAIL=fastci-test@example.com",
	)
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	runGitT(t, dir, "add", "-A")
	cmd := exec.Command("git", "commit", "-q", "-m", msg)
	cmd.Dir = dir
	cmd.Env = gitEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit (in %s): %v\n%s", dir, err, out)
	}
}

// newShallowFeatureClone builds a bare "remote" with a main branch and a
// feature branch that diverged from it, then advances main with its own
// independent commit (simulating the target branch moving on during a PR),
// and returns a fresh, genuinely shallow (--depth=1), single-ref clone of
// feature only - mirroring actions/checkout's default fetch-depth: 1
// behavior. It returns the clone's working directory.
func newShallowFeatureClone(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()

	bare := filepath.Join(tmp, "remote.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	seed := filepath.Join(tmp, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, seed, "init", "-q")
	runGitT(t, seed, "remote", "add", "origin", bare)
	writeFile(t, filepath.Join(seed, "base.txt"), "base\n")
	commitAll(t, seed, "base commit")
	runGitT(t, seed, "branch", "-M", "main")
	runGitT(t, seed, "push", "-q", "origin", "main")

	runGitT(t, seed, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(seed, "feature.txt"), "feature change\n")
	commitAll(t, seed, "feature: add feature.txt")
	runGitT(t, seed, "push", "-q", "origin", "feature")

	runGitT(t, seed, "checkout", "-q", "main")
	writeFile(t, filepath.Join(seed, "main-only.txt"), "main moved on\n")
	commitAll(t, seed, "main: advance independently after feature branched")
	runGitT(t, seed, "push", "-q", "origin", "main")

	clone := filepath.Join(tmp, "clone")
	// file:// is required for --depth to actually take effect on a local
	// path; a plain path clone silently ignores it.
	if out, err := exec.Command("git", "clone", "-q", "--depth=1", "--branch", "feature",
		"file://"+bare, clone).CombinedOutput(); err != nil {
		t.Fatalf("git clone --depth=1: %v\n%s", err, out)
	}

	if shallow := runGitT(t, clone, "rev-parse", "--is-shallow-repository"); shallow != "true\n" {
		t.Fatalf("test setup didn't produce a shallow clone: --is-shallow-repository = %q", shallow)
	}
	if _, err := exec.Command("git", "-C", clone, "rev-parse", "origin/main").CombinedOutput(); err == nil {
		t.Fatal("test setup: origin/main unexpectedly already resolves locally")
	}

	return clone
}

func TestChangedFilesRecoversFromShallowCloneMissingBaseHistory(t *testing.T) {
	clone := newShallowFeatureClone(t)

	files, err := gitdiff.ChangedFiles(clone, "origin/main")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	var names []string
	for _, f := range files {
		rel, err := filepath.Rel(clone, f)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, filepath.ToSlash(rel))
	}
	sort.Strings(names)

	want := []string{"feature.txt"}
	if len(names) != len(want) || names[0] != want[0] {
		t.Errorf("ChangedFiles = %v, want %v (main's own independent commit - main-only.txt - must not appear)", names, want)
	}
}

func TestChangedFilesErrorsClearlyWhenBaseTrulyDoesNotExist(t *testing.T) {
	clone := newShallowFeatureClone(t)

	_, err := gitdiff.ChangedFiles(clone, "origin/does-not-exist")
	if err == nil {
		t.Fatal("ChangedFiles = nil error, want an error for a nonexistent base ref")
	}
}
