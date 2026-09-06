package graph_test

import (
	"testing"

	"github.com/hpscript/fastci/internal/graph"
)

// Both tests simulate the same real-world event: a directory used to have
// two files (a.ts, b.ts), a.ts was deleted, and Build() re-scanned the
// filesystem, producing a graph with only b.ts's node left in that
// directory - the exact state impact analysis sees when resolving a.ts as
// a changed (now-deleted) file.

func TestTargetForFileDirFallbackResolvesDeletedSiblingByDefault(t *testing.T) {
	// This is the behavior Go/Cargo rely on: many files legitimately share
	// one node, so even a file no longer in the graph (deleted) resolves to
	// its directory's one remaining node.
	dir := t.TempDir()
	aFile, bFile := dir+"/a.ts", dir+"/b.ts"

	g := graph.New()
	g.Node(bFile).Files = []string{bFile}
	g.IndexFiles()

	target, ok := g.TargetForFile(aFile)
	if !ok || target != bFile {
		t.Errorf("TargetForFile(%q) = %q, %v; want %q, true", aFile, target, ok, bFile)
	}
}

func TestTargetForFileDirFallbackDisabled(t *testing.T) {
	// This is the fix: for file-granularity analyzers, a deleted file must
	// NOT resolve to an unrelated sibling that happens to be the only one
	// left in the same directory - misattributing the change that way can
	// silently skip a test that actually depended on the deleted file.
	dir := t.TempDir()
	aFile, bFile := dir+"/a.ts", dir+"/b.ts"

	g := graph.New()
	g.DisableDirFallback = true
	g.Node(bFile).Files = []string{bFile}
	g.IndexFiles()

	if _, ok := g.TargetForFile(aFile); ok {
		t.Error("TargetForFile resolved a deleted file via its directory despite DisableDirFallback - it must report unresolved instead")
	}
	// The surviving file must still resolve normally.
	if target, ok := g.TargetForFile(bFile); !ok || target != bFile {
		t.Errorf("TargetForFile(%q) = %q, %v; want %q, true", bFile, target, ok, bFile)
	}
}

func TestTargetForDir(t *testing.T) {
	// child's files live in dir/pkg, a subdirectory of parent's dir. Both
	// must resolve to their own node from their own directory - in
	// particular, querying dir/pkg must never fall back to parent, the way
	// TargetForFile(dir/pkg) would if pkg were mistaken for a filename and
	// stripped via filepath.Dir.
	dir := t.TempDir()
	parentFile := dir + "/parent.go"
	childFile := dir + "/pkg/child.go"

	g := graph.New()
	g.Node("parent").Files = []string{parentFile}
	g.Node("child").Files = []string{childFile}
	g.IndexFiles()

	if target, ok := g.TargetForDir(dir); !ok || target != "parent" {
		t.Errorf("TargetForDir(%q) = %q, %v; want %q, true", dir, target, ok, "parent")
	}
	if target, ok := g.TargetForDir(dir + "/pkg"); !ok || target != "child" {
		t.Errorf("TargetForDir(%q) = %q, %v; want %q, true", dir+"/pkg", target, ok, "child")
	}
	if _, ok := g.TargetForDir(dir + "/nonexistent"); ok {
		t.Error("TargetForDir resolved a directory with no files in it - want false")
	}
}

func TestTargetForDirDisabledByDirFallback(t *testing.T) {
	dir := t.TempDir()
	g := graph.New()
	g.DisableDirFallback = true
	g.Node("pkg").Files = []string{dir + "/f.ts"}
	g.IndexFiles()

	if _, ok := g.TargetForDir(dir); ok {
		t.Error("TargetForDir resolved a directory despite DisableDirFallback - it must report unresolved instead")
	}
}
