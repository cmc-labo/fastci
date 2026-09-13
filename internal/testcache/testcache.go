// Package testcache is a local, content-hash-keyed cache of test results:
// this machine's first step toward the "distributed build/dependency
// cache" from the project's Phase 1 roadmap (see the README) - only
// local-machine caching is implemented so far, not anything shared across
// machines.
//
// Re-running `fastci test` with exactly the same effective inputs for a
// target - its own files and everything it transitively depends on,
// content-for-content, plus the exact test-runner invocation - can skip
// actually re-executing it if the last recorded result for that same
// content was a pass. This is a genuinely different, more precise
// mechanism than the dependency-graph narrowing impact analysis already
// does: that narrowing decides "which targets could this diff possibly
// affect", while this cache asks "have we already seen this exact target
// content pass, regardless of how it got selected" - so it still finds
// something to skip even on a full run (a lockfile change, say, forces
// every target to be considered, but most of them likely didn't actually
// change content at all).
//
// Only a *passing* result is ever trusted: a cached failure is never used
// to skip a run, since doing so would hide a real, still-relevant problem
// instead of saving time. A target whose HasDynamicImport flag is set (or
// which transitively depends on one) is never eligible for caching
// either, for the same reason impact analysis always treats such a target
// as possibly affected: the graph can't prove it has captured the
// target's full dependency set, so a content hash over only the known
// files isn't a sound cache key.
package testcache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/hpscript/fastci/internal/graph"
)

const cacheFileName = "test-results.json"

// Cache is a local, on-disk record of {cache key -> last known result}.
type Cache struct {
	path    string
	entries map[string]entry
	dirty   bool
}

type entry struct {
	Passed bool `json:"passed"`
}

// Open loads the persisted cache from repoRoot's .fastci-cache directory,
// starting empty (not an error) if none exists yet or it's corrupt.
func Open(repoRoot string) *Cache {
	c := &Cache{
		path:    filepath.Join(repoRoot, ".fastci-cache", cacheFileName),
		entries: map[string]entry{},
	}
	if data, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(data, &c.entries) // corrupt cache -> just start empty
	}
	return c
}

// Filter splits targets into hits (a cached passing result exists for the
// exact current content of the target and everything it transitively
// depends on, under this exact analyzer/extraArgs invocation) and misses
// (everything else, which must actually be run).
func (c *Cache) Filter(g *graph.Graph, analyzerName string, extraArgs []string, targets []string) (hits, misses []string) {
	for _, t := range targets {
		key, ok := cacheKey(g, analyzerName, extraArgs, t)
		if ok {
			if e, found := c.entries[key]; found && e.Passed {
				hits = append(hits, t)
				continue
			}
		}
		misses = append(misses, t)
	}
	return hits, misses
}

// RecordPass marks every one of targets as passing under its current
// content hash, for a future run to reuse. Call this only after targets
// have actually been run and the run as a whole succeeded.
func (c *Cache) RecordPass(g *graph.Graph, analyzerName string, extraArgs []string, targets []string) {
	for _, t := range targets {
		key, ok := cacheKey(g, analyzerName, extraArgs, t)
		if !ok {
			continue // e.g. a dynamic-import target - never cached, see package doc.
		}
		c.entries[key] = entry{Passed: true}
		c.dirty = true
	}
}

// Save persists the cache to disk (only if anything changed via
// RecordPass), creating .fastci-cache and its .gitignore if needed.
func (c *Cache) Save() error {
	if !c.dirty {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	gitignore := filepath.Join(filepath.Dir(c.path), ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		_ = os.WriteFile(gitignore, []byte("*\n"), 0o644)
	}
	data, err := json.Marshal(c.entries)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), "test-results-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), c.path)
}

// cacheKey computes target's cache key from analyzerName, extraArgs, and a
// content hash over target and every node it transitively depends on (via
// Imports), sorted deterministically so the same dependency set always
// hashes the same way regardless of map iteration order. ok is false if
// target (or anything in its transitive closure) has HasDynamicImport set,
// or if any of their files can't be read.
func cacheKey(g *graph.Graph, analyzerName string, extraArgs []string, target string) (string, bool) {
	closure, ok := transitiveClosure(g, target)
	if !ok {
		return "", false
	}

	h := sha256.New()
	io.WriteString(h, analyzerName)
	h.Write([]byte{0})
	for _, a := range extraArgs {
		io.WriteString(h, a)
		h.Write([]byte{0})
	}
	h.Write([]byte{0})

	for _, id := range closure {
		n := g.Nodes[id]
		io.WriteString(h, id)
		h.Write([]byte{0})

		files := append([]string(nil), n.Files...)
		sort.Strings(files)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return "", false // can't hash reliably - never cache this target.
			}
			io.WriteString(h, f)
			h.Write([]byte{0})
			h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// transitiveClosure returns the sorted set of node IDs reachable from
// target via Imports (target's own dependencies, transitively), including
// target itself. ok is false if target or anything reachable from it has
// HasDynamicImport set, or doesn't exist in the graph at all.
func transitiveClosure(g *graph.Graph, target string) (ids []string, ok bool) {
	visited := map[string]bool{}
	var walk func(id string) bool
	walk = func(id string) bool {
		if visited[id] {
			return true
		}
		n := g.Nodes[id]
		if n == nil {
			return false
		}
		visited[id] = true
		if n.HasDynamicImport {
			return false
		}
		for dep := range n.Imports {
			if !walk(dep) {
				return false
			}
		}
		return true
	}
	if !walk(target) {
		return nil, false
	}
	ids = make([]string, 0, len(visited))
	for id := range visited {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, true
}
