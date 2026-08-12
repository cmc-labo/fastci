# fastci

**fastci** is a lightweight CI accelerator. Its first feature is the
**Impact-Driven Test Runner**: it looks at your git diff, builds a
dependency graph for your project, and runs your tests only against the
packages/files that actually changed or transitively depend on something
that changed — instead of your whole test suite.

No Bazel, no build-system migration. Drop it in front of your test runner
in your existing GitHub Actions workflow (or run it locally) and it gets
out of the way otherwise.

This is an early, incrementally-developed project. Today it covers:

- **Go** (`go test`) — package-level, module or [workspace](https://go.dev/ref/mod#workspaces)
- **TypeScript/JavaScript with Jest** — file-level

See [Roadmap](#roadmap) for what's next.

## How it works

1. `fastci test` auto-detects the project type in the working directory
   (Go module/workspace, or a Jest-configured `package.json`) and resolves
   the files changed in your working tree (or, with `--base`, the files
   changed between a base ref and `HEAD`).
2. It builds a dependency graph using the **real language tooling**, not
   regex/string matching over import statements:
   - Go: [`go/packages`](https://pkg.go.dev/golang.org/x/tools/go/packages)
     (backed by `go list`), which resolves module paths, `internal/`
     visibility, `replace` directives, and `go.work` workspaces exactly the
     way the Go toolchain itself would.
   - Jest: [`esbuild`](https://esbuild.github.io/)'s resolver, which
     understands relative imports, `tsconfig.json` `paths`/`baseUrl`
     aliases, and extension/index resolution — the same way your bundler
     would resolve them.
3. It builds a reverse dependency index from that graph: for every
   package/file, what imports it (directly or transitively) — including
   edges that only exist through test files.
4. Changed files are mapped to their owning node, then the reverse index is
   walked to find every node that could be affected, and only that subset
   of tests is run. If a change can't be safely attributed (e.g. a
   manifest/lockfile changed, or a changed source file can't be resolved),
   fastci falls back to running the full suite rather than silently
   skipping something that matters.

## Install

```sh
go install github.com/hpscript/fastci/cmd/fastci@latest
```

## Usage

Run from the project root — a Go module (`go.mod`), a Go workspace
(`go.work`), or a Jest project (`package.json` with Jest configured):

```sh
# Test whatever's affected by your uncommitted changes
fastci test

# Test what's affected between the target branch and HEAD (PR-style diff)
fastci test --base origin/main

# See what would run without running it
fastci test --dry-run -v

# Bypass impact analysis and run everything
fastci test --all

# Forward flags to the underlying test runner
fastci test -- -race -v      # go test
fastci test -- --coverage    # jest
```

Example output (Go):

```
$ fastci test --base origin/main
fastci: selected 3/42 test target(s) (go, 93% skipped)
  * internal/parser
    internal/parser/lexer
    cmd/yourtool
ok  	github.com/you/yourrepo/internal/parser	0.004s
ok  	github.com/you/yourrepo/internal/parser/lexer	0.002s
ok  	github.com/you/yourrepo/cmd/yourtool	0.011s
```

Example output (Jest, with a `tsconfig.json` path alias in the mix):

```
$ fastci test --dry-run -v
fastci: 1 changed file(s):
  src/leaf.ts
fastci: selected 3/4 test target(s) (jest, 25% skipped)
    src/consumer.test.ts
    src/leaf.test.ts
    src/mid.test.ts
fastci: dry-run, not executing tests
```

Lines marked `*` are packages/files that changed directly; unmarked lines
are pulled in transitively because they depend on something that changed.

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

      # Go projects
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      # Jest projects
      # - uses: actions/setup-node@v4
      #   with:
      #     node-version: 22
      # - run: npm ci

      - name: Fetch base branch
        run: git fetch origin ${{ github.base_ref }} --depth=1

      - run: go install github.com/hpscript/fastci/cmd/fastci@latest
      - run: fastci test --base origin/${{ github.base_ref }}
```

## Current limitations

**Go**
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

**Jest**
- Bare specifiers that resolve into `node_modules` are treated as external
  and are not walked further. In an npm/pnpm/yarn **workspace monorepo**,
  a cross-package import like `import {x} from '@myorg/utils'` is
  currently **not** tracked as a graph edge (relative imports and
  `tsconfig.json` `paths`/`baseUrl` aliases within a single package *are*
  fully resolved). This mirrors where Go started before workspace support
  was added, and is the natural next increment for Jest.
- Test-file discovery uses Jest's default conventions
  (`*.test.{js,jsx,ts,tsx,mjs,cjs}`, `*.spec.{...}`, or anything under
  `__tests__/`). Custom `testMatch`/`testRegex` overrides in a
  `jest.config.*`/`package.json` `"jest"` field aren't honored yet — such
  a project still works, but test-file classification falls back to the
  defaults.
- `jest.config.js/.ts/.mjs/.cjs` (JS-computed config) isn't parsed for any
  purpose beyond "this file changing forces a full run"; only
  `jest.config.json` and the `package.json` `"jest"` field are read.
- Ambient `.d.ts` files are ignored (no runtime effect on tests).

## Roadmap

This tracks the phased plan in the project design doc:

- **Phase 1 (V1.0)** — Impact-Driven Test Runner (this) + a distributed
  build/dependency cache.
- **Phase 2 (V1.5)** — `fastci analyze`: AI-assisted failure log analysis
  and fix suggestions.
- **Phase 3 (V2.0)** — `fastci local` (fast local CI reproduction) and
  `fastci guard` (supply-chain / runtime security guardrails).

Language coverage grows incrementally alongside this. Candidates being
considered next: Jest monorepo/workspace cross-package resolution, Python
(pytest), and Rust (Cargo).

## License

MIT — see [LICENSE](LICENSE).
