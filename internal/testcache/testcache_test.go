package testcache_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/testcache"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildGraph constructs a small graph mirroring a real analyzer's output:
// leaf <- mid <- consumer (consumer imports mid, mid imports leaf), each
// with one real file on disk so cacheKey can hash real content.
func buildGraph(t *testing.T, dir string) *graph.Graph {
	t.Helper()
	leaf := filepath.Join(dir, "leaf.go")
	mid := filepath.Join(dir, "mid.go")
	consumer := filepath.Join(dir, "consumer.go")
	writeFile(t, leaf, "package p\nfunc Leaf() string { return \"leaf\" }\n")
	writeFile(t, mid, "package p\nfunc Mid() string { return Leaf() }\n")
	writeFile(t, consumer, "package p\nfunc Consumer() string { return Mid() }\n")

	g := graph.New()
	g.Node("leaf").Files = []string{leaf}
	g.Node("mid").Files = []string{mid}
	g.Node("mid").Imports["leaf"] = true
	g.Node("consumer").Files = []string{consumer}
	g.Node("consumer").Imports["mid"] = true
	g.IndexFiles()
	g.BuildImporters()
	return g
}

func TestFilterMissesEverythingWithNoCache(t *testing.T) {
	dir := t.TempDir()
	g := buildGraph(t, dir)
	c := testcache.Open(dir) // no .fastci-cache exists yet

	hits, misses := c.Filter(g, "go", nil, []string{"leaf", "mid", "consumer"})
	if len(hits) != 0 {
		t.Errorf("hits = %v, want none on a fresh cache", hits)
	}
	if len(misses) != 3 {
		t.Errorf("misses = %v, want all 3 targets", misses)
	}
}

func TestRecordPassThenFilterHitsOnUnchangedContent(t *testing.T) {
	dir := t.TempDir()
	g := buildGraph(t, dir)
	c := testcache.Open(dir)

	c.RecordPass(g, "go", nil, []string{"leaf", "mid", "consumer"})
	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A fresh Cache (simulating the next `fastci test` invocation) loaded
	// from disk must recognize the unchanged content as cache hits.
	c2 := testcache.Open(dir)
	hits, misses := c2.Filter(g, "go", nil, []string{"leaf", "mid", "consumer"})
	if len(misses) != 0 {
		t.Errorf("misses = %v, want none - nothing changed since RecordPass", misses)
	}
	if len(hits) != 3 {
		t.Errorf("hits = %v, want all 3 targets", hits)
	}
}

func TestFilterMissesWhenDependencyContentChanges(t *testing.T) {
	dir := t.TempDir()
	g := buildGraph(t, dir)
	c := testcache.Open(dir)
	c.RecordPass(g, "go", nil, []string{"leaf", "mid", "consumer"})
	c.Save()

	// Edit leaf.go's content directly (not via buildGraph, which would
	// reset all three files back to their original text) - consumer and
	// mid transitively depend on it, so both their cache keys must change
	// too, even though their own files weren't touched. The graph
	// structure itself (nodes/edges) is unchanged, exactly as a real
	// analyzer's Build() would report after only a file's content, not its
	// imports, changed.
	writeFile(t, filepath.Join(dir, "leaf.go"), "package p\nfunc Leaf() string { return \"leaf-CHANGED\" }\n")

	c2 := testcache.Open(dir)
	hits, misses := c2.Filter(g, "go", nil, []string{"leaf", "mid", "consumer"})
	for _, want := range []string{"leaf", "mid", "consumer"} {
		found := false
		for _, m := range misses {
			if m == want {
				found = true
			}
		}
		if !found {
			t.Errorf("misses = %v, want %q to be a miss (it or a dependency changed)", misses, want)
		}
	}
	if len(hits) != 0 {
		t.Errorf("hits = %v, want none - leaf.go changed, invalidating everything that depends on it", hits)
	}
}

func TestFilterMissesWhenExtraArgsChange(t *testing.T) {
	dir := t.TempDir()
	g := buildGraph(t, dir)
	c := testcache.Open(dir)
	c.RecordPass(g, "go", []string{"-v"}, []string{"leaf"})
	c.Save()

	c2 := testcache.Open(dir)
	_, misses := c2.Filter(g, "go", []string{"-race"}, []string{"leaf"})
	if len(misses) != 1 {
		t.Errorf("misses = %v, want a miss when extraArgs differ from what was recorded", misses)
	}
}

func TestFilterNeverCachesDynamicImportTargets(t *testing.T) {
	dir := t.TempDir()
	g := buildGraph(t, dir)
	g.Nodes["mid"].HasDynamicImport = true
	c := testcache.Open(dir)

	c.RecordPass(g, "go", nil, []string{"leaf", "mid", "consumer"})
	c.Save()

	c2 := testcache.Open(dir)
	hits, misses := c2.Filter(g, "go", nil, []string{"leaf", "mid", "consumer"})
	wantMiss := map[string]bool{"mid": true, "consumer": true} // consumer transitively depends on mid.
	for _, h := range hits {
		if wantMiss[h] {
			t.Errorf("hits = %v, want %q to never be cached (HasDynamicImport, directly or transitively)", hits, h)
		}
	}
	if len(misses) != 2 {
		t.Errorf("misses = %v, want exactly [mid consumer]", misses)
	}
}
