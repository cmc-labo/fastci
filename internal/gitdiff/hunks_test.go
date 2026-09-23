package gitdiff_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/gitdiff"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	return dir
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

func writeAndCommit(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", msg)
}

func TestChangedHunksReportsAddedLineRange(t *testing.T) {
	dir := initRepo(t)
	writeAndCommit(t, dir, "f.txt", "line1\nline2\nline3\n", "init")

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\nNEW\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ranges, ok, err := gitdiff.ChangedHunks(dir, "", filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(ranges) != 1 || ranges[0] != (gitdiff.LineRange{Start: 3, End: 3}) {
		t.Errorf("ranges = %v, want [{3 3}]", ranges)
	}
}

func TestChangedHunksReportsMultiLineRange(t *testing.T) {
	dir := initRepo(t)
	writeAndCommit(t, dir, "f.txt", "a\nb\nc\nd\ne\n", "init")

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nB2\nC2\nD2\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ranges, ok, err := gitdiff.ChangedHunks(dir, "", filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(ranges) != 1 || ranges[0] != (gitdiff.LineRange{Start: 2, End: 4}) {
		t.Errorf("ranges = %v, want [{2 4}]", ranges)
	}
}

func TestChangedHunksReportsMultipleHunks(t *testing.T) {
	dir := initRepo(t)
	writeAndCommit(t, dir, "f.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n", "init")

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("1\nCHANGED\n3\n4\n5\n6\n7\n8\nCHANGED2\n10\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ranges, ok, err := gitdiff.ChangedHunks(dir, "", filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(ranges) != 2 {
		t.Fatalf("ranges = %v, want 2 hunks", ranges)
	}
	if ranges[0] != (gitdiff.LineRange{Start: 2, End: 2}) {
		t.Errorf("ranges[0] = %v, want {2 2}", ranges[0])
	}
	if ranges[1] != (gitdiff.LineRange{Start: 9, End: 9}) {
		t.Errorf("ranges[1] = %v, want {9 9}", ranges[1])
	}
}

func TestChangedHunksPureDeletionIsNotOK(t *testing.T) {
	dir := initRepo(t)
	writeAndCommit(t, dir, "f.txt", "1\n2\n3\n4\n5\n", "init")

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("1\n2\n4\n5\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := gitdiff.ChangedHunks(dir, "", filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("ok = true for a pure-deletion hunk, want false")
	}
}

func TestChangedHunksUntrackedFileIsNotOK(t *testing.T) {
	dir := initRepo(t)
	writeAndCommit(t, dir, "keep.txt", "x\n", "init")

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("brand new file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := gitdiff.ChangedHunks(dir, "", filepath.Join(dir, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("ok = true for an untracked new file, want false (git diff shows nothing for it)")
	}
}

func TestChangedHunksWithBaseRef(t *testing.T) {
	dir := initRepo(t)
	writeAndCommit(t, dir, "f.txt", "a\nb\nc\n", "init")
	runGit(t, dir, "branch", "base-branch")

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\nCHANGED\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-q", "-m", "change")

	ranges, ok, err := gitdiff.ChangedHunks(dir, "base-branch", filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if len(ranges) != 1 || ranges[0] != (gitdiff.LineRange{Start: 2, End: 2}) {
		t.Errorf("ranges = %v, want [{2 2}]", ranges)
	}
}
