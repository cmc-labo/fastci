# fastci

**fastci** is a lightweight CI accelerator. Its first feature is the
**Impact-Driven Test Runner**: it looks at your git diff, builds a
package-level dependency graph for your Go module, and runs `go test` only
against the packages that actually changed or transitively depend on
something that changed — instead of your whole test suite.

No Bazel, no build-system migration. Drop it in front of `go test` in your
existing GitHub Actions workflow (or run it locally) and it gets out of the
way otherwise.

This is an early, incrementally-developed project. Today it covers Go /
`go test`. See [Roadmap](#roadmap) for what's next.

## How it works

1. `fastci test` resolves the files changed in your working tree (or, with
   `--base`, the files changed between a base ref and `HEAD`).
2. It loads your module's package graph with `go/packages` and builds a
   reverse dependency index: for every package, which packages import it
   (directly or transitively) — including edges that only exist through
   `_test.go` files.
3. Changed files are mapped to their owning package, then the reverse index
   is walked to find every package that could be affected.
4. `go test` runs against exactly that set. If a change can't be safely
   attributed to a package (e.g. `go.mod`/`go.sum` changed, or a changed
   `.go` file can't be resolved), fastci falls back to running the full
   suite rather than silently skipping something that matters.

## Install

```sh
go install github.com/hpscript/fastci/cmd/fastci@latest
```

## Usage

Run from the root of a Go module (a `go.mod` must be present in the working
directory):

```sh
# Test whatever's affected by your uncommitted changes
fastci test

# Test what's affected between the target branch and HEAD (PR-style diff)
fastci test --base origin/main

# See what would run without running it
fastci test --dry-run -v

# Bypass impact analysis and run everything
fastci test --all

# Forward flags to `go test`
fastci test -- -race -v
```

Example output:

```
$ fastci test --base origin/main
fastci: selected 3/42 test package(s) (93% skipped)
  * github.com/you/yourrepo/internal/parser
    github.com/you/yourrepo/internal/parser/lexer
    github.com/you/yourrepo/cmd/yourtool
ok  	github.com/you/yourrepo/internal/parser	0.004s
ok  	github.com/you/yourrepo/internal/parser/lexer	0.002s
ok  	github.com/you/yourrepo/cmd/yourtool	0.011s
```

Lines marked `*` are packages that changed directly; unmarked lines are
packages pulled in transitively because they depend on something that
changed.

## GitHub Actions

```yaml
name: CI
on:
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # fastci needs full history to diff against the base branch

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: Fetch base branch
        run: git fetch origin ${{ github.base_ref }} --depth=1

      - run: go install github.com/hpscript/fastci/cmd/fastci@latest
      - run: fastci test --base origin/${{ github.base_ref }}
```

## Current limitations

- Run `fastci` from either a Go module root (`go.mod` present) or a Go
  workspace root (`go.work` present, listing member modules as
  subdirectories). Cross-module import edges within a workspace are
  resolved correctly, including test-only edges. `go.work`/`go.work.sum`
  changes trigger a full run, same as `go.mod`/`go.sum`.
- Impact analysis is package-level, not function-level, for now (see
  [Roadmap](#roadmap)).
- Non-Go changes (docs, workflow YAML, etc.) are treated as not affecting
  any test package. Go files that reference non-Go inputs at build time
  (e.g. `//go:embed`) aren't tracked yet.

## Roadmap

This tracks the phased plan in the project design doc:

- **Phase 1 (V1.0)** — Impact-Driven Test Runner (this) + a distributed
  build/dependency cache.
- **Phase 2 (V1.5)** — `fastci analyze`: AI-assisted failure log analysis
  and fix suggestions.
- **Phase 3 (V2.0)** — `fastci local` (fast local CI reproduction) and
  `fastci guard` (supply-chain / runtime security guardrails).

## License

MIT — see [LICENSE](LICENSE).
