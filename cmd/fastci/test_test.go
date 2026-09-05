package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hpscript/fastci/internal/analyzer/goanalyzer"
	"github.com/hpscript/fastci/internal/graph"
)

// buildGraphWithFiles constructs a minimal graph where the given nodes each
// own the listed number of files, for exercising fullRunThresholdReason's
// file-counting independent of any real analyzer's Build().
func buildGraphWithFiles(t *testing.T, filesPerNode map[string]int) *graph.Graph {
	t.Helper()
	g := graph.New()
	for id, n := range filesPerNode {
		files := make([]string, n)
		for i := range files {
			files[i] = id + "/f" + string(rune('a'+i)) + ".go"
		}
		g.Node(id).Files = files
	}
	return g
}

func TestFullRunThresholdReasonBelowThreshold(t *testing.T) {
	g := buildGraphWithFiles(t, map[string]int{"pkg": 10})
	changed := []string{"pkg/fa.go"} // 1/10 = 10%
	a := goanalyzer.New()

	if _, ok := fullRunThresholdReason(g, changed, a, 20); ok {
		t.Error("fullRunThresholdReason = true, want false (10% is below the 20% threshold)")
	}
}

func TestFullRunThresholdReasonAtOrAboveThreshold(t *testing.T) {
	g := buildGraphWithFiles(t, map[string]int{"pkg": 10})
	changed := []string{"pkg/fa.go", "pkg/fb.go"} // 2/10 = 20%
	a := goanalyzer.New()

	reason, ok := fullRunThresholdReason(g, changed, a, 20)
	if !ok {
		t.Fatal("fullRunThresholdReason = false, want true (20% meets the 20% threshold)")
	}
	if reason == "" {
		t.Error("fullRunThresholdReason returned an empty reason string")
	}
}

func TestFullRunThresholdReasonIgnoresNonTrackedFiles(t *testing.T) {
	g := buildGraphWithFiles(t, map[string]int{"pkg": 10})
	// README.md is Ignorable for goanalyzer (not a .go file), so it must not
	// count toward the changed-file numerator even though it's in the diff.
	changed := []string{"README.md"}
	a := goanalyzer.New()

	if _, ok := fullRunThresholdReason(g, changed, a, 1); ok {
		t.Error("fullRunThresholdReason = true, want false (the only changed file is Ignorable, not a tracked source file)")
	}
}

func TestFullRunThresholdReasonEmptyGraph(t *testing.T) {
	g := graph.New()
	a := goanalyzer.New()

	if _, ok := fullRunThresholdReason(g, []string{"anything.go"}, a, 1); ok {
		t.Error("fullRunThresholdReason = true, want false for an empty graph (no tracked files to divide by)")
	}
}

// runGitT runs git in dir, failing the test on error.
func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

// TestRunTestWhyWithAllDoesNotRunTests guards against a real bug: --why
// documents itself as diagnostic-only ("doesn't run any tests"), but --all
// bypasses impact analysis and returns before the --why handling was ever
// reached, silently ignoring --why and running the entire suite instead of
// just explaining.
func TestRunTestWhyWithAllDoesNotRunTests(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module whyallcheck\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkga"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkga", "pkga.go"), []byte("package pkga\nfunc A() string { return \"a\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkga", "pkga_test.go"),
		[]byte("package pkga\nimport \"testing\"\nfunc TestA(t *testing.T) { if A() != \"a\" { t.Fatal(\"bad\") } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, dir, "init", "-q")
	runGitT(t, dir, "add", "-A")
	cmd := exec.Command("git", "commit", "-q", "-m", "init")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fastci-test", "GIT_AUTHOR_EMAIL=fastci-test@example.com",
		"GIT_COMMITTER_NAME=fastci-test", "GIT_COMMITTER_EMAIL=fastci-test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWD)

	stdout := captureStdout(t, func() {
		err := runTest(&cobra.Command{}, testOpts{all: true, why: "pkga/pkga.go"})
		if err != nil {
			t.Fatalf("runTest: %v", err)
		}
	})

	if !strings.Contains(stdout, "nothing to explain") {
		t.Errorf("stdout = %q, want it to mention there's nothing to explain under --all", stdout)
	}
	if strings.Contains(stdout, "ok") || strings.Contains(stdout, "PASS") {
		t.Errorf("stdout = %q, want no test-execution output - --why must not let --all actually run tests", stdout)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}
