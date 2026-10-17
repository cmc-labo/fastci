package testcache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/graph"
)

// buildOneNodeGraph is a minimal graph with one real file on disk, enough
// for cacheKey to hash successfully.
func buildOneNodeGraph(t *testing.T, dir string) *graph.Graph {
	t.Helper()
	leaf := filepath.Join(dir, "leaf.go")
	if err := os.WriteFile(leaf, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	g.Node("leaf").Files = []string{leaf}
	g.IndexFiles()
	g.BuildImporters()
	return g
}

// panickingRemote is a *remoteCache whose get/put genuinely panic (a nil
// *http.Client - verified directly: calling Do() on one panics with a nil
// pointer dereference, the same failure shape a real bug in remote.go
// could plausibly cause), used to prove Filter/RecordPass survive a panic
// from their background remote-cache goroutines rather than crashing the
// whole process - the exact guarantee remote.go's own package doc
// promises ("every call is best-effort... never fails the actual test
// run"). Without the recover() in testcache.go's own goroutines, either
// test below crashes the entire test binary, not just fails - an
// unrecovered panic in a non-main goroutine terminates the process.
func panickingRemote() *remoteCache {
	return &remoteCache{baseURL: "http://example.invalid"}
}

func TestFilterSurvivesPanickingRemoteGet(t *testing.T) {
	dir := t.TempDir()
	g := buildOneNodeGraph(t, dir)
	c := &Cache{entries: map[string]entry{}, remote: panickingRemote()}

	hits, misses := c.Filter(g, "go", nil, []string{"leaf"})
	if len(hits) != 0 {
		t.Errorf("hits = %v, want none - a panicking remote lookup must be treated like a miss, not silently a hit", hits)
	}
	if len(misses) != 1 || misses[0] != "leaf" {
		t.Errorf("misses = %v, want [\"leaf\"]", misses)
	}
}

func TestRecordPassSurvivesPanickingRemotePut(t *testing.T) {
	dir := t.TempDir()
	g := buildOneNodeGraph(t, dir)
	c := &Cache{entries: map[string]entry{}, remote: panickingRemote()}

	// Reaching this line at all (not crashing the test binary) is the
	// real assertion here.
	c.RecordPass(g, "go", nil, []string{"leaf"})

	if !c.dirty {
		t.Error("dirty = false, want true - the local record should still succeed despite the remote put panicking")
	}
	if len(c.entries) != 1 {
		t.Errorf("entries = %v, want exactly one local entry recorded", c.entries)
	}
}
