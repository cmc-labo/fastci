# Benchmarks

Real, reproducible measurements of `fastci test` against synthetic Go
repositories of varying size, run on this machine and committed alongside
the scripts that produced them. No numbers here are estimated, projected,
or drawn from a real company's private CI — everything is generated and
measured by the scripts in this directory, which anyone can re-run.

```sh
docs/benchmarks/generate.sh <dir> <N>              # generate an N-package synthetic repo
docs/benchmarks/run_bench.sh <N> [repeats]         # the common case (see below)
docs/benchmarks/run_bench_worstcase.sh <N> [repeats]  # the worst case (see below)
```

## Methodology

Each run generates a synthetic Go module with `N` independent `featureNNN`
packages, all depending on one shared `internal/common` package — a
common shape for a real, large Go monorepo: many mostly-independent
feature packages sharing a handful of low-level utility packages. See
`generate.sh` for the exact generated code.

**The common case** (`run_bench.sh`): a body-only edit to a single
`feature1` package's source file — a realistic day-to-day change that
doesn't touch any other package's files, left uncommitted so `fastci test`
picks it up the same way a developer running it locally, mid-change,
would. Measures:

- baseline: `go clean -testcache && go test ./...`
- fastci: `go clean -testcache && rm -rf .fastci-cache && fastci test`

Both start from a cold test cache and a cold fastci cache on every
repeat, so neither measurement benefits from a previous run's caching —
this is meant to represent a fresh CI checkout, not a warm local rerun
(where fastci's [local test-result cache](../../README.md#test-result-cache-local-and-distributed)
would help further still). `fastci` itself is built fresh from the
checked-out source before each run.

**The worst case** (`run_bench_worstcase.sh`): a body-only edit to the
*shared* `internal/common` package instead — every feature package
genuinely, correctly depends on it, so impact analysis is expected to
(correctly) select every single package, the same as `go test ./...`
would run. This case exists to show what happens when narrowing
*doesn't* apply, not just the cases where it shines.

Every number below is the median of 3 repeats, with all 3 raw
measurements shown — real variance included, not hidden. This ran on a
single, modest, shared 2-vCPU / 2 GB-RAM sandbox VM (`go1.25.0
linux/arm64`), not dedicated hardware; absolute times will differ on your
own machine or CI runner, and a memory-constrained environment like this
one is part of why the N=1000 baseline numbers below have wide swings.
Re-run the scripts yourself for numbers that reflect your own hardware.

## Results: the common case (one independent package changed)

| N (packages) | `go test ./...` (median) | `fastci test` (median) | speedup | packages actually run |
| --: | --: | --: | --: | --: |
| 10 | 0.93s | 1.82s | 0.5x (slower) | 1 / 11 |
| 20 | 1.76s | 1.66s | 1.1x | 1 / 21 |
| 50 | 2.93s | 1.94s | 1.5x | 1 / 51 |
| 200 | 11.91s | 2.29s | 5.2x | 1 / 201 |
| 500 | 31.40s | 7.28s | 4.3x | 1 / 501 |
| 1000 | 119.07s | 5.57s | 21.3x | 1 / 1001 |

Raw per-repeat times (seconds), oldest run first:

| N | baseline repeats | fastci repeats |
| --: | --- | --- |
| 10 | 1.57, 0.93, 0.77 | 2.19, 1.82, 1.43 |
| 20 | 3.10, 1.52, 1.76 | 2.96, 1.59, 1.66 |
| 50 | 3.71, 2.56, 2.93 | 1.94, 2.18, 1.76 |
| 200 | 15.00, 11.33, 11.91 | 3.46, 2.29, 1.84 |
| 500 | 37.36, 30.28, 31.40 | 9.44, 7.28, 6.35 |
| 1000 | 166.69, 119.07, 62.46 | 5.60, 5.57, 4.45 |

**fastci has real, fixed per-invocation overhead** — loading package
metadata via `go/packages`, building the dependency graph, computing
impact — that a tiny repo's saved test time doesn't cover. Below roughly
10-20 packages in this environment, `fastci test` breaks even or is
*slower* than just running everything; that crossover point will differ
by machine and by how expensive your actual tests are (these synthetic
tests do almost no real work, so this is close to a worst case for the
overhead side of the comparison). Past that point the benefit grows with
repo size, since a one-package change touches a roughly constant number
of packages regardless of how many exist in total, while the baseline's
cost grows with the whole repo.

## Results: the worst case (shared dependency changed)

| N (packages) | `go test ./...` (median) | `fastci test` (median) | ratio | packages actually run |
| --: | --: | --: | --: | --: |
| 200 | 33.45s | 36.67s | 1.0x | 201 / 201 (full run, correctly) |

Raw repeats (seconds): baseline 34.65, 33.45, 27.17 — fastci 38.84, 30.42,
36.67.

When narrowing genuinely doesn't apply, fastci doesn't make things
worse — it adds its own fixed analysis overhead (visible here as roughly
a 10% difference, itself within the run-to-run noise this environment
shows) on top of running the exact same full suite `go test ./...` would.

## On ease of adoption

Every measurement above ran through the same `fastci test` invocation
described in the main [README](../../README.md#github-actions): dropped
in front of an existing `go test` step in a GitHub Actions workflow with
no build-system migration, no Bazel, no separate service to run. See
that section for the actual workflow YAML diff.
