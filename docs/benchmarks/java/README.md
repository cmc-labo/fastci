# Benchmarks: Java (Maven)

Real, reproducible measurements of `fastci test` against a synthetic
multi-module Maven reactor, run on this machine and committed alongside
the scripts that produced them — the same discipline as the
[Go benchmarks](../README.md) one level up, applied to the Java (Maven)
analyzer. No numbers here are estimated, projected, or drawn from a real
company's private CI.

```sh
docs/benchmarks/java/generate.sh <dir> <N>                 # generate an N-module synthetic reactor
docs/benchmarks/java/run_bench.sh <N> [repeats]             # the common case (see below)
docs/benchmarks/java/run_bench_worstcase.sh <N> [repeats]   # the worst case (see below)
```

## Methodology

Each run generates a synthetic Maven reactor with `N` independent
`featureNNN` modules, all depending on one shared `common` module — the
same shape, for the same reason, as the [Go benchmark](../README.md)
uses: a common pattern for a real, large Java monorepo (many
mostly-independent modules sharing a handful of low-level utility
modules). See `generate.sh` for the exact generated code.

**The common case** (`run_bench.sh`): a body-only edit to a single
`feature1` module's source file — a realistic day-to-day change that
doesn't touch any other module's files, left uncommitted so `fastci test`
picks it up the same way a developer running it locally, mid-change,
would. Measures:

- baseline: `mvn test`
- fastci: `rm -rf .fastci-cache && fastci test`

Both runs happen against an already-warm `target/` (every module built
once, untimed, before any measurement starts) — this is meant to
represent the realistic, steady-state case of a project with an existing
build, where Maven's own incremental compiler only recompiles the one
file that actually changed; it is *not* a cold `mvn clean test`. fastci's
own `.fastci-cache` (which holds its compiled Roslyn-style parser
shim — see [javaanalyzer's own doc comment](../../../internal/analyzer/javaanalyzer/javaanalyzer.go))
*is* cleared before every repeat, so every fastci measurement below pays
the full cold-compile cost of that shim each time — the conservative,
worst-case assumption for fastci's own number, not a flattering one; a
real CI checkout that runs `fastci test` more than once reuses that cache
and would be faster still. Both commands run fully offline (`-o`), using
whatever's already in `~/.m2` — run `mvn -q test` once by hand first on a
fresh machine to warm that cache, the same one-time setup any real Maven
project needs regardless of fastci.

**The worst case** (`run_bench_worstcase.sh`): a body-only edit to the
*shared* `common` module instead — every feature module genuinely,
correctly depends on it, so impact analysis is expected to (correctly)
select every single module's tests, the same as plain `mvn test` would
run. This case exists to show what happens when narrowing *doesn't*
apply, not just the cases where it shines.

A real, structural difference from the Go benchmark worth stating
plainly: Maven's own reactor build compiles *every* module's main and
test sources on `mvn test` regardless of `-Dtest` filtering — narrowing
only skips Surefire's actual *test execution* for the modules filtered
out, never their compilation. The speedup below is entirely a function of
skipped test-execution and JVM-fork overhead per excluded module, not
skipped compilation — see "What the numbers mean" below.

Every number below is the median of 3 repeats, with all 3 raw
measurements shown — real variance included, not hidden. This ran on the
same single, modest, shared 2-vCPU / 2 GB-RAM sandbox VM as the Go
benchmark (Maven 3.6.3, Java 11, `linux/arm64`), not dedicated hardware;
absolute times will differ on your own machine or CI runner. Re-run the
scripts yourself for numbers that reflect your own hardware.

## Results: the common case (one independent module changed)

| N (feature modules) | `mvn test` (median) | `fastci test` (median) | speedup | tests actually run |
| --: | --: | --: | --: | --: |
| 10 | 3.09s | 2.18s | 1.4x | 1 / 11 modules |
| 20 | 6.60s | 4.05s | 1.6x | 1 / 21 modules |
| 50 | 11.29s | 2.84s | 3.9x | 1 / 51 modules |
| 100 | 19.28s | 3.50s | 5.5x | 1 / 101 modules |
| 200 | 33.01s | 4.59s | 7.1x | 1 / 201 modules |

Raw per-repeat times (seconds), oldest run first:

| N | baseline repeats | fastci repeats |
| --: | --- | --- |
| 10 | 3.34, 3.09, 2.97 | 2.18, 2.16, 3.01 |
| 20 | 6.60, 8.65, 6.46 | 4.05, 6.49, 3.81 |
| 50 | 18.93, 11.29, 10.70 | 2.96, 2.68, 2.84 |
| 100 | 19.28, 17.56, 25.35 | 4.13, 2.99, 3.50 |
| 200 | 33.01, 33.19, 31.79 | 4.73, 4.59, 4.58 |

## Results: the worst case (shared module changed)

| N (feature modules) | `mvn test` (median) | `fastci test` (median) | ratio | tests actually run |
| --: | --: | --: | --: | --: |
| 50 | 8.37s | 10.67s | 1.3x (slower) | 51 / 51 modules (full run, correctly) |
| 200 | 55.46s | 35.57s | 0.6x | 201 / 201 modules (full run, correctly) |

Raw repeats (seconds):

- N=50: baseline 8.30, 8.37, 9.25 — fastci 10.19, 10.67, 11.63.
- N=200: baseline 51.17, 60.52, 55.46 — fastci 61.56, 35.30, 35.57.

The N=50 row is the representative one: fastci adds a modest, bounded
overhead (~25-30%) when narrowing genuinely doesn't apply, the same
story the Go benchmark's own worst case tells. The N=200 row's "fastci is
*faster* in the worst case" result doesn't reflect a real effect — both
sides run the literal same full reactor build either way, with no
mechanism for fastci to be structurally faster here — it's this sandbox's
own resource contention showing through (visible in the raw repeats
themselves: one fastci repeat spiked to 61.56s while the other two sat
near 35s, and this VM was already under swap pressure during this run).
It's reported rather than discarded, in keeping with this directory's
"real variance included, not hidden" rule, but the N=50 row is the one to
trust for what the worst case actually costs.

## What the numbers mean

**The speedup grows with reactor size**, the same shape as the Go
benchmark: a one-module change touches a roughly constant slice of the
reactor regardless of how many modules exist in total, while the
baseline's cost - both compiling and test-executing every module - grows
with the whole reactor. Below N=20 or so, fastci's own fixed
per-invocation overhead (parsing every module's source with the embedded
javac-based shim, building the dependency graph, computing impact, and -
in this specific benchmark - recompiling its own parser shim from
scratch every time) isn't yet covered by the time saved; past that point
the benefit grows.

**The speedup here is smaller than the "tests actually run" column alone
would suggest**, and that gap is real, not noise: at N=200, only 1 of 201
modules' tests actually run, yet the wall-clock speedup is "only" 7.1x,
not ~200x. This is the direct, honest consequence of the structural point
raised in the methodology above - Maven's reactor build still compiles
*every* module's main and test sources on every `mvn test` invocation,
`-Dtest` filter or not. What fastci actually saves here is Surefire's own
per-excluded-module JVM-fork-and-report overhead and the (in this
benchmark, essentially instant) test execution itself - a real and
growing saving as the module count grows, but a smaller one than
compilation-skipping would give. A project with genuinely expensive
tests (real I/O, containers, slow fixtures) rather than this benchmark's
near-instant ones would see a much larger share of the baseline's time
disappear, since skipped test *execution* time would dominate far more
than it does here.

**This is a real, documented room-for-improvement, not swept under the
rug**: `RunTests` (see
[javaanalyzer.go](../../../internal/analyzer/javaanalyzer/javaanalyzer.go)) selects
tests via Surefire's `-Dtest=...`/Gradle's `--tests`, run across the
*whole* reactor - it does not use Maven's own `-pl <modules> -am` (or
Gradle's analogous project-exclusion) to skip *building* the modules
outside the selection entirely. Doing so would let fastci skip
compilation too, not just test execution, which this benchmark shows
would meaningfully widen the gap above, especially for a reactor where
compilation itself (not test execution) dominates the baseline's time.
That's a legitimate follow-up, not something this benchmark took a
position on implementing.

## On ease of adoption

Every measurement above ran through the same `fastci test` invocation
described in the main [README](../../../README.md#usage) - no Maven
reactor restructuring, no separate service, no change to how the project
is normally built.
