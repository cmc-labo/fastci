package testcache_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hpscript/fastci/internal/graph"
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

// newTransportFailingServer returns a server that fails every request at
// the transport level - it hijacks the raw connection and closes it
// without ever writing an HTTP response - rather than responding with an
// ordinary (if unsuccessful) HTTP status. That distinction matters: only
// a transport-level failure (the client can't complete the round trip at
// all) should trip the circuit breaker; an HTTP error response from a
// reachable server deliberately should not (see recordOutcome in
// remote.go). attempts counts every request the server actually received,
// so a test can tell a real network attempt from one the circuit breaker
// skipped.
func newTransportFailingServer(t *testing.T) (attempts *atomic.Int32, srv *httptest.Server) {
	t.Helper()
	attempts = &atomic.Int32{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter doesn't support hijacking")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	return attempts, srv
}

// manyLeafGraph builds n independent, distinct-content leaf nodes (no
// imports, no shared dependencies) so each gets its own distinct cache
// key and none can be satisfied by another's lookup - unlike buildGraph's
// fixed 3-node chain, this needs to comfortably exceed remoteConcurrency
// to actually exercise the circuit breaker across concurrent workers.
func manyLeafGraph(t *testing.T, dir string, n int) (*graph.Graph, []string) {
	t.Helper()
	g := graph.New()
	targets := make([]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("leaf%d", i)
		targets[i] = id
		f := filepath.Join(dir, id+".go")
		writeFile(t, f, fmt.Sprintf("package p\nfunc Leaf%d() int { return %d }\n", i, i))
		g.Node(id).Files = []string{f}
	}
	return g, targets
}

// TestFilterCircuitBreakerBoundsAttemptsAgainstADeadRemote reproduces a
// real robustness gap: without a circuit breaker, a remote that's slow to
// fail (here, simulated as failing every single request, which is the
// worst case) gets a fresh network attempt for every target that misses
// locally, one remoteConcurrency batch at a time - for a large target
// set, that's a real, multi-request stall even though each individual
// call is "best-effort". The fix should bound the number of actual
// network attempts to roughly one concurrency batch, regardless of how
// many targets are being filtered.
func TestFilterCircuitBreakerBoundsAttemptsAgainstADeadRemote(t *testing.T) {
	attempts, srv := newTransportFailingServer(t)
	t.Setenv("FASTCI_REMOTE_CACHE_URL", srv.URL)

	const targetCount = 40
	dir := t.TempDir()
	g, targets := manyLeafGraph(t, dir, targetCount)
	c := testcache.Open(dir)

	hits, misses := c.Filter(g, "go", nil, targets)
	if len(hits) != 0 {
		t.Errorf("hits = %v, want none from a dead remote", hits)
	}
	if len(misses) != targetCount {
		t.Errorf("misses = %d, want all %d targets", len(misses), targetCount)
	}

	got := attempts.Load()
	if got >= int32(targetCount) {
		t.Errorf("remote received %d requests for %d targets - the circuit breaker should have stopped attempting the remote well before every target got its own network round trip", got, targetCount)
	}
	t.Logf("remote received %d actual network attempts for %d targets", got, targetCount)
}
