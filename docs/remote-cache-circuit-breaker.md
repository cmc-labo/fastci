# The remote cache's circuit breaker

This explains the circuit breaker added to fastci's distributed
([`FASTCI_REMOTE_CACHE_URL`](../README.md#test-result-cache-local-and-distributed))
test-result cache: how it works, how to configure it (short answer: you
don't need to - see below), and what it actually changes for a user who
has a remote cache configured. Everything below is verified against the
current implementation in
[`internal/testcache/remote.go`](../internal/testcache/remote.go) and
against real, measured runs of the actual `fastci` CLI - not just the
source comments.

## The problem it fixes

The remote cache is documented as best-effort: a network error, a
timeout, or the remote being entirely unreachable degrades to "just don't
use the remote cache" for that run, never failing the build. Before this
circuit breaker existed, "degrades to" was true in terms of *correctness*
but not quite true in terms of *time*: every single target that missed
the local cache still got its own fresh network attempt, each bounded by
the remote HTTP client's own 5-second timeout
(`remoteCacheTimeout`), up to 8 at a time (`remoteConcurrency`).

That distinction matters a lot for *which way* a remote fails. A remote
that actively refuses the connection (wrong port, nothing listening)
fails in milliseconds - no timeout is ever actually hit, so this was
already fast with or without a circuit breaker. But a remote that's
merely **slow to fail** - a firewall silently dropping packets instead of
sending a TCP reset, a load balancer that accepts a connection and then
never responds - makes every single request actually sit out the full
5-second timeout. For a large target set, that adds up: 40 targets at 8
concurrent requests per batch is 5 batches, each paying the full timeout
in the worst case - a real multi-minute stall despite every individual
call being "best-effort" and never actually failing the build.

## How it works

Each `remoteCache` (one per `Cache`, one per `fastci test`/`fastci local`
invocation - see [Scope](#scope-and-lifetime) below) tracks a simple
failure counter and a tripped flag, both guarded by a mutex since Filter
and RecordPass issue requests from up to 8 goroutines concurrently:

- Every GET or PUT first checks whether the breaker has already tripped.
  If so, it returns immediately with an internal `errRemoteUnreachable`
  error **without attempting any network call at all**.
- Otherwise, it performs the HTTP request as normal, then records the
  outcome:
  - **The round trip completed** - the server was reached and returned
    some HTTP response, *regardless of status code* - resets the failure
    counter to zero. An unexpected status (wrong token → 403, a typo'd
    path → 404 from the wrong kind of server, a 500) is a **configuration
    problem**, not an unreachable remote, and is reported through the
    existing single-warning-per-run mechanism (see
    [Observability](#what-you-actually-see) below) - it deliberately does
    **not** count toward tripping the breaker, since giving up on every
    other target because of one misconfigured response would be the wrong
    call.
  - **The round trip failed at the transport level** - a timeout,
    connection refused, DNS failure, anything where the client never got
    a response to parse at all - increments the counter. Once that
    counter reaches `maxConsecutiveFailures` (currently **3**), the
    breaker trips: every GET/PUT for the rest of this run short-circuits
    from then on.

Because `Filter` (checking the cache) and `RecordPass` (writing to it
after a successful run) share the same `*Cache`/`*remoteCache` instance
within one `fastci test` invocation, a breaker tripped during `Filter`
stays tripped for the later `RecordPass` call too - there's no point
attempting to push results to a remote that was just confirmed
unreachable moments earlier in the same run.

### Scope and lifetime

The breaker's state lives entirely on one in-memory `remoteCache` value,
created fresh by `testcache.Open` at the start of each `fastci
test`/`fastci local` invocation. It is **not** persisted anywhere (not to
the local `.fastci-cache/test-results.json`, not to any other file) and
carries no memory across runs: if the remote was unreachable on one run,
the very next invocation starts with a clean counter and will try the
remote again from scratch. A remote that comes back up is noticed on the
next run automatically - there's nothing to reset manually.

## Configuration

**There is no configuration for this.** `maxConsecutiveFailures` (3) is a
fixed constant in `internal/testcache/remote.go`, not an environment
variable or flag - there is no `FASTCI_REMOTE_CACHE_*` setting to raise,
lower, or disable it. The only configuration that exists for the remote
cache at all is whether to use one in the first place:

```sh
export FASTCI_REMOTE_CACHE_URL=https://cache.example.com/fastci
export FASTCI_REMOTE_CACHE_TOKEN=...   # optional, sent as a bearer token
```

Once a remote is configured this way, the circuit breaker is **always
active**, automatically, with no opt-out. This is a deliberate choice
consistent with the rest of the remote cache's design (also not
configurable: the 5-second timeout, the concurrency of 8) - it's internal
plumbing in service of the documented "best-effort, never blocks the
run" contract, not a tunable knob users are expected to reach for.

## What you actually see

The breaker tripping is **silent** beyond its effect on timing - this is
worth knowing explicitly, since it's easy to assume there'd be a distinct
log line for it. In practice:

- The very first transport-level failure prints fastci's existing,
  single warning for the whole run (further errors are suppressed by
  design, to avoid a flaky remote spamming a line per target):
  ```
  fastci: remote cache: checking remote cache: Get "http://cache.example.com/fastci/<key>": context deadline exceeded (Client.Timeout exceeded while awaiting headers) (further remote cache errors this run are suppressed)
  ```
- Once the breaker trips (a few requests later), nothing further is
  printed about it - subsequent targets are just quietly treated as cache
  misses, same as if no remote were configured at all. There is currently
  no separate "giving up on the remote cache for this run" message; the
  only user-visible evidence that the breaker specifically kicked in
  (rather than the remote merely being consistently, individually
  unreachable request after request) is that the run finishes
  noticeably faster than attempting every target would have.
- Test **correctness is entirely unaffected**. Every target the breaker
  causes to short-circuit is treated exactly like an ordinary cache miss:
  it's actually run, not skipped. The breaker only removes a
  doomed-to-fail *shortcut attempt* - it never causes a test that should
  run to be skipped, and never causes a real pass to be misreported.

One real, minor consequence worth knowing: if the remote happens to
recover *in the middle of* a run where the breaker already tripped, this
run won't notice - it won't try the remote again until the *next*
invocation. A pass recorded locally during a network-degraded run still
gets pushed to the remote on some later run once a fresh `remoteCache` is
constructed and nothing trips it, just not necessarily the one where the
remote happened to come back.

## Measured impact

These numbers come from actually running the real `fastci` binary
against a 20-package Go module (`fastci test --dry-run -v`, forcing a
full run via a `go.mod` touch so all 20 targets hit the cache), pointed at
three different kinds of remote. They're a demonstration from one
machine, not a formal benchmark - the shape of the result (bounded vs.
linearly-growing-with-target-count) is the point, not the exact seconds.

| Remote condition | Without the breaker | With the breaker |
| --- | --- | --- |
| Refuses the connection immediately (dead port) | ~0.25s | ~0.25s (unaffected either way - already fast) |
| Accepts the connection but never responds (simulated silent firewall drop) | ~15.4s (3 full 5s timeouts, one per concurrency batch) | ~10.1s (trips partway through, well before every target gets its own attempt) |

The gap between the two "accepts but never responds" numbers is the
breaker's entire value proposition, and it **widens with target count** -
the "without" column scales as `ceil(targets / 8) × 5s`, unbounded as a
repository grows, while the "with" column stays roughly flat regardless
of how many targets are being filtered, since only the first batch or so
ever actually attempts the network call before the breaker fires.

To reproduce this yourself: point `FASTCI_REMOTE_CACHE_URL` at a TCP
listener that accepts connections and never writes a response (a trivial
Python `socket`/`accept`/`sleep` loop works), run `fastci test --dry-run
-v` against any project with enough independent test targets to exceed
8, and compare against pointing it at a port nothing listens on at all.

## See also

- [Test-result cache (local and distributed)](../README.md#test-result-cache-local-and-distributed)
  in the main README - the remote cache's own full reference (the
  GET/PUT protocol, the bearer token, how a remote hit folds into the
  local cache).
- [`internal/testcache/remote.go`](../internal/testcache/remote.go) - the
  actual implementation this document describes.
