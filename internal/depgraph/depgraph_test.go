package depgraph_test

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/hpscript/fastci/internal/depgraph"
)

func sampleModDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplemod"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadBuildsForwardAndReverseEdges(t *testing.T) {
	g, err := depgraph.Load(sampleModDir(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantNodes := []string{
		"samplemod/pkga", "samplemod/pkgb", "samplemod/pkgc",
		"samplemod/pkgd", "samplemod/pkge",
	}
	for _, n := range wantNodes {
		if _, ok := g.Nodes[n]; !ok {
			t.Errorf("missing node %s", n)
		}
	}

	// pkga -> pkgb -> pkgc
	if !g.Nodes["samplemod/pkga"].Imports["samplemod/pkgb"] {
		t.Error("pkga should import pkgb")
	}
	if !g.Nodes["samplemod/pkgb"].Imports["samplemod/pkgc"] {
		t.Error("pkgb should import pkgc")
	}
	if len(g.Nodes["samplemod/pkgc"].Imports) != 0 {
		t.Errorf("pkgc should have no imports, got %v", g.Nodes["samplemod/pkgc"].Imports)
	}

	// pkge only depends on pkgd through its _test.go file.
	if !g.Nodes["samplemod/pkge"].Imports["samplemod/pkgd"] {
		t.Error("pkge should have a test-only import edge to pkgd")
	}

	wantImporters := map[string][]string{
		"samplemod/pkgc": {"samplemod/pkgb"},
		"samplemod/pkgb": {"samplemod/pkga"},
		"samplemod/pkgd": {"samplemod/pkge"},
	}
	for target, want := range wantImporters {
		got := append([]string(nil), g.Importers[target]...)
		sort.Strings(got)
		sort.Strings(want)
		if !equal(got, want) {
			t.Errorf("Importers[%s] = %v, want %v", target, got, want)
		}
	}

	for _, n := range wantNodes {
		if !g.Nodes[n].HasTestFiles {
			t.Errorf("node %s should have test files", n)
		}
	}
}

func TestTargetForFile(t *testing.T) {
	dir := sampleModDir(t)
	g, err := depgraph.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	f := filepath.Join(dir, "pkgb", "pkgb.go")
	target, ok := g.TargetForFile(f)
	if !ok || target != "samplemod/pkgb" {
		t.Errorf("TargetForFile(%s) = %q, %v; want samplemod/pkgb, true", f, target, ok)
	}

	if _, ok := g.TargetForFile(filepath.Join(dir, "nope.go")); ok {
		t.Error("TargetForFile should fail for an unknown file")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
