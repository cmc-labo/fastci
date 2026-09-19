package testcache

import (
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
// without a flaky or down remote spamming a line per target.
type remoteCache struct {
	baseURL string
	token   string
	client  *http.Client

	warnOnce sync.Once
}

const remoteCacheTimeout = 5 * time.Second

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
	req, err := http.NewRequest(method, r.baseURL+"/"+key, body)
	if err != nil {
		return nil, err
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	return r.client.Do(req)
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
