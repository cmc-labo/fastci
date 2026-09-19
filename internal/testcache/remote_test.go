package testcache_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/hpscript/fastci/internal/testcache"
)

// fakeRemoteStore is a minimal in-memory stand-in for whatever real
// service FASTCI_REMOTE_CACHE_URL would point at in production: GET
// returns 200 if the key was PUT before, 404 otherwise.
type fakeRemoteStore struct {
	mu    sync.Mutex
	keys  map[string]bool
	token string // if set, requests must carry "Authorization: Bearer <token>"

	putCount int
	getCount int
}

func newFakeRemoteStore(t *testing.T) (*fakeRemoteStore, *httptest.Server) {
	t.Helper()
	s := &fakeRemoteStore{keys: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		key := r.URL.Path
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			s.getCount++
			if s.keys[key] {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodPut:
			s.putCount++
			s.keys[key] = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

// TestFilterHitsAcrossMachinesViaRemoteCache is the core distributed-cache
// scenario: two independent local caches (simulating two different
// machines, or a dev machine and a CI runner) sharing one remote. Machine
// A records a pass and pushes it to the remote; machine B, whose own local
// cache starts completely empty, must still see a hit for the identical
// target content by falling back to the remote.
func TestFilterHitsAcrossMachinesViaRemoteCache(t *testing.T) {
	_, srv := newFakeRemoteStore(t)
	t.Setenv("FASTCI_REMOTE_CACHE_URL", srv.URL)

	dirA := t.TempDir()
	gA := buildGraph(t, dirA)
	cA := testcache.Open(dirA)
	cA.RecordPass(gA, "go", nil, []string{"leaf", "mid", "consumer"})
	if err := cA.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dirB := t.TempDir()
	gB := buildGraph(t, dirB) // identical file contents -> identical cache keys
	cB := testcache.Open(dirB)
	hits, misses := cB.Filter(gB, "go", nil, []string{"leaf", "mid", "consumer"})
	if len(misses) != 0 {
		t.Errorf("misses = %v, want none - B's empty local cache should still hit via the shared remote", misses)
	}
	if len(hits) != 3 {
		t.Errorf("hits = %v, want all 3 targets, served from the remote", hits)
	}
}

// TestFilterPopulatesLocalCacheFromRemoteHit checks the write-through
// behavior: once B has seen a remote hit, a second Filter call against the
// same (now-reopened) local cache must not need the remote again.
func TestFilterPopulatesLocalCacheFromRemoteHit(t *testing.T) {
	store, srv := newFakeRemoteStore(t)
	t.Setenv("FASTCI_REMOTE_CACHE_URL", srv.URL)

	dirA := t.TempDir()
	gA := buildGraph(t, dirA)
	cA := testcache.Open(dirA)
	cA.RecordPass(gA, "go", nil, []string{"leaf"})
	cA.Save()

	dirB := t.TempDir()
	gB := buildGraph(t, dirB)
	cB := testcache.Open(dirB)
	if _, misses := cB.Filter(gB, "go", nil, []string{"leaf"}); len(misses) != 0 {
		t.Fatalf("first Filter: misses = %v, want a remote hit", misses)
	}
	if err := cB.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	store.mu.Lock()
	getsAfterFirstFilter := store.getCount
	store.mu.Unlock()

	// Reopen B's local cache (a fresh Cache, like the next `fastci test`
	// invocation) and filter again - this must be a local hit, not another
	// remote round trip.
	cB2 := testcache.Open(dirB)
	if _, misses := cB2.Filter(gB, "go", nil, []string{"leaf"}); len(misses) != 0 {
		t.Fatalf("second Filter: misses = %v, want a hit from the now-populated local cache", misses)
	}

	store.mu.Lock()
	getsAfterSecondFilter := store.getCount
	store.mu.Unlock()
	if getsAfterSecondFilter != getsAfterFirstFilter {
		t.Errorf("remote GET count grew from %d to %d on the second Filter call, want no new remote lookups (should have been a local hit)",
			getsAfterFirstFilter, getsAfterSecondFilter)
	}
}

func TestRemoteCacheHonorsBearerToken(t *testing.T) {
	store, srv := newFakeRemoteStore(t)
	store.token = "s3cr3t"
	t.Setenv("FASTCI_REMOTE_CACHE_URL", srv.URL)
	t.Setenv("FASTCI_REMOTE_CACHE_TOKEN", "s3cr3t")

	dirA := t.TempDir()
	gA := buildGraph(t, dirA)
	cA := testcache.Open(dirA)
	cA.RecordPass(gA, "go", nil, []string{"leaf"})
	cA.Save()

	dirB := t.TempDir()
	gB := buildGraph(t, dirB)
	cB := testcache.Open(dirB)
	if _, misses := cB.Filter(gB, "go", nil, []string{"leaf"}); len(misses) != 0 {
		t.Errorf("misses = %v, want a remote hit with the correct bearer token", misses)
	}
}

func TestRemoteCacheWrongTokenIsTreatedAsMiss(t *testing.T) {
	store, srv := newFakeRemoteStore(t)
	store.token = "s3cr3t"
	t.Setenv("FASTCI_REMOTE_CACHE_URL", srv.URL)
	t.Setenv("FASTCI_REMOTE_CACHE_TOKEN", "wrong-token")

	dirA := t.TempDir()
	gA := buildGraph(t, dirA)
	cA := testcache.Open(dirA)
	cA.RecordPass(gA, "go", nil, []string{"leaf"})
	cA.Save() // A's own local file still records the pass regardless of the remote push failing.

	dirB := t.TempDir()
	gB := buildGraph(t, dirB)
	cB := testcache.Open(dirB)
	_, misses := cB.Filter(gB, "go", nil, []string{"leaf"})
	if len(misses) != 1 {
		t.Errorf("misses = %v, want a miss - the remote should reject the wrong token, not silently authenticate", misses)
	}
}

// TestFilterFallsBackToMissWhenRemoteUnreachable ensures a down/misconfigured
// remote degrades to "just don't use the cache", not a hang or a crash.
func TestFilterFallsBackToMissWhenRemoteUnreachable(t *testing.T) {
	// Port 1 should reliably refuse the connection immediately rather than
	// hang until remoteCacheTimeout elapses, keeping this test fast.
	t.Setenv("FASTCI_REMOTE_CACHE_URL", "http://127.0.0.1:1")

	dir := t.TempDir()
	g := buildGraph(t, dir)
	c := testcache.Open(dir)
	hits, misses := c.Filter(g, "go", nil, []string{"leaf", "mid", "consumer"})
	if len(hits) != 0 {
		t.Errorf("hits = %v, want none with an unreachable remote", hits)
	}
	if len(misses) != 3 {
		t.Errorf("misses = %v, want all 3 targets", misses)
	}
}

func TestRecordPassPushesToRemote(t *testing.T) {
	store, srv := newFakeRemoteStore(t)
	t.Setenv("FASTCI_REMOTE_CACHE_URL", srv.URL)

	dir := t.TempDir()
	g := buildGraph(t, dir)
	c := testcache.Open(dir)
	c.RecordPass(g, "go", nil, []string{"leaf", "mid", "consumer"})

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.putCount != 3 {
		t.Errorf("remote PUT count = %d, want 3 (one per target)", store.putCount)
	}
}
