package testcache

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// remoteCache is the "distributed" half of Phase 1's build/dependency
// cache roadmap item: a minimal HTTP GET/PUT key-value protocol - the same
// shape Bazel's remote cache, sccache, and similar tools use - so fastci
// can point at literally anything that speaks HTTP GET/PUT for a key under
// a base URL (a small self-hosted server, a static-file host with PUT
// support, an S3-compatible bucket via presigned URLs a CI job generates,
// ...) without fastci itself needing an opinion on, or an SDK for, what's
// actually on the other end.
//
// It's entirely opt-in, activated only by setting the
// FASTCI_REMOTE_CACHE_URL environment variable (mirroring how fastci
// analyze only activates once ANTHROPIC_API_KEY is set) - most users never
// touch it, and its absence isn't an error.
//
// Every call is best-effort: a network error, timeout, or unexpected
// status talking to the remote never fails the actual test run - it's
// treated the same as a cache miss (or "the pass didn't get recorded
// remotely", for a failed Put), with one warning printed to stderr per run
// so a persistently unreachable remote doesn't fail silently forever, but
// without a flaky or down remote spamming a line per target. A circuit
// breaker (see maxConsecutiveFailures) additionally stops attempting the
// remote at all for the rest of the run after enough consecutive
// transport-level failures, so a down-but-slow-to-fail remote degrades to
// a bounded delay, not a real multi-minute stall across a large target
// set.
type remoteCache struct {
	baseURL string
	token   string
	client  *http.Client

	warnOnce sync.Once

	// mu guards failures/unreachable, the circuit breaker below.
	mu          sync.Mutex
	failures    int
	unreachable bool
}

const remoteCacheTimeout = 5 * time.Second

// maxConsecutiveFailures is the circuit breaker's trip threshold: once
// this many consecutive requests fail at the transport level (couldn't
// even complete the round trip - a timeout, connection refused, DNS
// failure - as opposed to a request that reached the server and got back
// an ordinary HTTP status), do is short-circuited for the rest of this
// Cache's lifetime (one `fastci test`/`fastci local` invocation) instead
// of still attempting - and waiting out remoteCacheTimeout for - every
// remaining target. Without this, a remote that's merely slow to fail
// (e.g. a firewall silently dropping packets rather than refusing the
// connection outright) degrades "just don't use the remote cache" from a
// documented, cheap no-op into a real multi-minute stall on a large
// target set, one remoteCacheTimeout at a time, remoteConcurrency
// requests at a time - which defeats the entire point of this being
// best-effort. A request that reaches the server at all (any HTTP
// response, even an error one) resets the counter: that's a config
// problem (wrong token, wrong path), not an unreachable remote, and
// doesn't warrant giving up on every other target too.
const maxConsecutiveFailures = 3

// errRemoteUnreachable is returned by do once the circuit breaker has
// tripped, short-circuiting without attempting the network call at all.
var errRemoteUnreachable = errors.New("remote cache: too many consecutive failures, no longer attempting to reach it this run")

// remoteFromEnv builds a remoteCache from FASTCI_REMOTE_CACHE_URL (and
// optional FASTCI_REMOTE_CACHE_TOKEN, sent as a bearer token), or returns
// nil if the URL isn't set.
func remoteFromEnv() *remoteCache {
	url := strings.TrimSuffix(strings.TrimSpace(os.Getenv("FASTCI_REMOTE_CACHE_URL")), "/")
	if url == "" {
		return nil
	}
	return &remoteCache{
		baseURL: url,
		token:   os.Getenv("FASTCI_REMOTE_CACHE_TOKEN"),
		client:  &http.Client{Timeout: remoteCacheTimeout},
	}
}

func (r *remoteCache) do(method, key string, body io.Reader) (*http.Response, error) {
	if r.tripped() {
		return nil, errRemoteUnreachable
	}
	req, err := http.NewRequest(method, r.baseURL+"/"+key, body)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	r.recordOutcome(err == nil)
	return resp, err
}

// tripped reports whether the circuit breaker has already fired.
func (r *remoteCache) tripped() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unreachable
}

// recordOutcome updates the circuit breaker: ok is whether the request
// completed the round trip at all (reaching the server and getting back
// some HTTP response, regardless of status code), not whether that
// response was a cache hit/success.
func (r *remoteCache) recordOutcome(ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ok {
		r.failures = 0
		return
	}
	r.failures++
	if r.failures >= maxConsecutiveFailures {
		r.unreachable = true
	}
}

// get reports whether key has a recorded pass on the remote.
func (r *remoteCache) get(key string) bool {
	resp, err := r.do(http.MethodGet, key, nil)
	if err != nil {
		r.warn(fmt.Sprintf("checking remote cache: %v", err))
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		r.warn(fmt.Sprintf("remote cache GET %s: unexpected status %s", key, resp.Status))
	}
	return resp.StatusCode == http.StatusOK
}

// put best-effort records key as a pass on the remote.
func (r *remoteCache) put(key string) {
	resp, err := r.do(http.MethodPut, key, strings.NewReader("pass"))
	if err != nil {
		r.warn(fmt.Sprintf("writing to remote cache: %v", err))
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		r.warn(fmt.Sprintf("remote cache PUT %s: unexpected status %s", key, resp.Status))
	}
}

func (r *remoteCache) warn(msg string) {
	r.warnOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "fastci: remote cache: %s (further remote cache errors this run are suppressed)\n", msg)
	})
}

// remoteConcurrency bounds how many simultaneous GET/PUT requests Filter
// and RecordPass issue against the remote - high enough that a large
// target set doesn't pay for network round trips one at a time, low
// enough not to look like abuse to whatever's on the other end.
const remoteConcurrency = 8
