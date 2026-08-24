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
- **TypeScript/JavaScript with Vitest** — file-level
- **TypeScript/JavaScript with Jest** — file-level
- **Python with pytest** — file-level
- **Rust with Cargo** — crate-level, single crate or [workspace](https://doc.rust-lang.org/cargo/reference/workspaces.html)

See [Roadmap](#roadmap) for what's next.

## How it works

1. `fastci test` auto-detects the project type in the working directory
   (Go module/workspace, a Vitest-configured project, a Jest-configured
   `package.json`, a pytest-configured Python project, or a Rust
   crate/Cargo workspace) and resolves the files changed in your working
   tree (or, with `--base`, the files changed between a base ref and
   `HEAD`).
2. It builds a dependency graph using the **real language tooling**, not
   regex/string matching over import statements:
   - Go: [`go/packages`](https://pkg.go.dev/golang.org/x/tools/go/packages)
     (backed by `go list`), which resolves module paths, `internal/`
     visibility, `replace` directives, and `go.work` workspaces exactly the
     way the Go toolchain itself would.
   - Vitest and Jest: [`esbuild`](https://esbuild.github.io/)'s resolver,
     which understands relative imports, `tsconfig.json` `paths`/`baseUrl`
     aliases, extension/index resolution, and `import()` calls with a
     static string argument — the same way your bundler would resolve
     them. A Jest `moduleNameMapper` config (from `jest.config.json` or
     `package.json`'s `"jest"` field) is additionally applied through a
     custom esbuild resolver plugin, so aliases defined only there (not in
     `tsconfig.json`) are tracked too; Vitest has no equivalent yet — see
     [Current limitations](#current-limitations).
   - pytest: every `.py` file is parsed with Python's own `ast` module, and
     import targets (including relative imports like `from ..pkg import x`)
     are normalized with the stdlib's `importlib.util.resolve_name`, then
     matched against a registry of every file's dotted module name built by
     walking the project tree. This never imports/executes the project's
     own code — see [Current limitations](#current-limitations) for what
     that trades off.
   - Cargo: [`cargo metadata`](https://doc.rust-lang.org/cargo/commands/cargo-metadata.html),
     which resolves the real crate dependency graph (path dependencies,
     normal/dev/build dependencies) the same way `cargo build`/`cargo test`
     would.
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
(`go.work`), a Vitest project (`vitest.config.*` or a `vitest` dependency),
a Jest project (`package.json` with Jest configured), a pytest project
(`pytest.ini`, `conftest.py`, or a
`[tool.pytest.ini_options]`/`[tool:pytest]` section), or a Rust crate or
Cargo workspace (`Cargo.toml`):

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
fastci test -- -race -v        # go test
fastci test -- --coverage      # vitest run / jest
fastci test -- -x -k foo       # pytest
fastci test -- --no-fail-fast  # cargo test
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

Example output (Vitest, with a `tsconfig.json` path alias in the mix):

```
$ fastci test --dry-run -v
fastci: 1 changed file(s):
  src/leaf.ts
fastci: selected 3/5 test target(s) (vitest, 40% skipped)
    src/consumer.test.ts
    src/leaf.test.ts
    src/mid.test.ts
fastci: dry-run, not executing tests
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

Example output (pytest, with a relative import crossing a package
boundary):

```
$ fastci test --dry-run -v
fastci: 1 changed file(s):
  src/mypkg/leaf.py
fastci: selected 3/4 test target(s) (pytest, 25% skipped)
    tests/test_consumer.py
    tests/test_leaf.py
    tests/test_mid.py
fastci: dry-run, not executing tests
```

Example output (Cargo, a path-dependency chain across a workspace):

```
$ fastci test --dry-run -v
fastci: 1 changed file(s):
  crates/leaf/src/lib.rs
fastci: selected 3/4 test target(s) (cargo, 25% skipped)
    consumer
  * leaf
    mid
fastci: dry-run, not executing tests
```

Lines marked `*` are packages/files that changed directly; lines marked `~`
contain an import fastci can't statically resolve (see the Vitest/Jest/pytest
dynamic-import notes under [Current limitations](#current-limitations)) and
are always included as a safety net; unmarked lines are pulled in
transitively because they depend on something that changed (directly, or
via a `~` line).

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

      # Vitest/Jest projects
      # - uses: actions/setup-node@v4
      #   with:
      #     node-version: 22
      # - run: npm ci

      # pytest projects
      # - uses: actions/setup-python@v5
      #   with:
      #     python-version: "3.12"
      # - run: pip install -r requirements.txt

      # Cargo projects: actions-rs or dtolnay/rust-toolchain, or nothing
      # if the runner image already ships a toolchain.

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

**Vitest**
- Shares its esbuild-based import resolution, dynamic `import()`/`require()`
  handling, and `node_modules`/workspace-monorepo limitation with Jest (see
  below) — everything in the Jest section below other than the
  `moduleNameMapper`/`jest.config.*` points applies to Vitest too.
- Vite's own `resolve.alias` config (in `vite.config.*`/`vitest.config.*`)
  is **not** resolved — unlike Jest's `moduleNameMapper`, which is a
  JSON-shaped value that can be read as data, a Vite alias list lives
  inside arbitrary JS/TS config code with no static format to parse. An
  import resolved only through such an alias is invisible to the graph;
  `tsconfig.json` `paths`/`baseUrl` aliases (which esbuild resolves
  directly) are unaffected by this and work as expected.
- Test-file discovery uses Vitest's default `include` pattern
  (`**/*.{test,spec}.?(c|m)[jt]sx?`). A custom `test.include`/`test.exclude`
  in `vitest.config.*` isn't honored yet — such a project still works, but
  test-file classification falls back to the default.
- Any `vitest.config.*` or `vite.config.*` change forces a full run (same
  treatment as `jest.config.*` for Jest), since either can change aliases,
  plugins, or test settings the import graph can't see.

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
  `jest.config.json` and the `package.json` `"jest"` field are read. This
  also means a `moduleNameMapper` defined only in a JS-computed config
  (rather than `jest.config.json`) isn't picked up.
- Ambient `.d.ts` files are ignored (no runtime effect on tests).
- **Dynamic `import()`/`require()` with a runtime-computed argument** (a
  variable, a function call, string concatenation, etc.) can't be resolved
  by esbuild or any other static tool — there's no way to know which file
  it'll load without actually running the code. fastci detects these call
  sites with a lightweight source scan and marks the containing file `~`
  (see the output legend above): it's always included in the selected test
  set whenever *anything* in the project changes, rather than only when
  something it statically depends on changed, trading away some of the "%
  skipped" narrowing for soundness. A template-literal argument with a
  static directory prefix (`` import(`./plugins/${name}`) ``) is a special
  case: if that directory exists, fastci resolves it to real edges against
  every file under it (a safe superset, since the exact match can't be
  known without running the code) instead of marking the file `~`; if the
  directory doesn't exist, the file is marked `~` and the build no longer
  fails outright (an earlier limitation). Dynamic imports with a **static
  string literal** argument (`import("./foo")`, including ones resolved
  through `tsconfig.json` paths or `moduleNameMapper`) are fully tracked,
  same as a regular `import` statement, and never marked `~`.

**pytest**
- Import resolution is entirely static (AST parsing + dotted-name matching
  against files discovered on disk) and never imports the project's own
  code, unlike a naive `importlib.util.find_spec` approach — this avoids
  executing arbitrary `__init__.py` side effects or requiring dependencies
  to be installed just to build the graph. **Dynamic imports**
  (`importlib.import_module(...)`, bare `__import__(...)`) are detected —
  the argument isn't inspected, even a literal is treated the same as a
  computed one — and the containing file is marked `~` (see the output
  legend above): it's always included in the selected test set whenever
  anything in the project changes, same safety-net semantics as Jest's
  dynamic `import()` handling, rather than trying to resolve the call's
  actual target. Plugin/entry-point style loading through some other
  indirection, and symbols re-exported through a package's `__init__.py`
  from somewhere non-obvious, aren't detected at all.
- Source roots are the project directory and, if present, a top-level
  `src/` directory (covering both flat and `src` layouts). Other custom
  layouts (e.g. a `package_dir` remapping in `setup.cfg`) aren't read.
- `conftest.py` changing anywhere forces a full run, since fixture scope
  and `autouse` effects aren't something the import graph captures safely.
- Test-file discovery uses pytest's default conventions (`test_*.py` /
  `*_test.py`). Custom `python_files`/`python_classes`/`python_functions`
  overrides in `pytest.ini`/`pyproject.toml`/`setup.cfg` aren't honored
  yet — such a project still works, but test-file classification falls
  back to the defaults.
- Building the graph requires a `python3` (or `python`) interpreter on
  `PATH`; running tests additionally looks for `.venv/bin/pytest`,
  `venv/bin/pytest`, or `env/bin/pytest` before falling back to `pytest`/
  `python3 -m pytest` on `PATH`.

**Cargo**
- Granularity is per-crate (like Go's per-package), not per-test-function:
  any change to a crate reruns `cargo test -p <crate>` for every crate
  reachable through it in the reverse dependency graph. Unit tests
  (`#[cfg(test)]` modules embedded in `src/`) and integration tests
  (`tests/*.rs`) are both covered, since they belong to the same crate.
- A crate is considered to "have tests" (and so shows up in target counts
  and gets actually run) if it has a `tests/` directory or any `#[test]`
  attribute found via a lightweight source scan — not a full parse, so an
  unusually-formatted attribute (e.g. built by a macro) could be missed;
  worst case that crate is silently skipped from `--all`/full-run target
  *counts* only; `cargo test -p` is still always safe to run against it
  either way.
- Any `Cargo.toml` changing — the workspace root's or any single crate's —
  forces a full run, since dependency/feature changes can ripple in ways
  the resolved graph snapshot alone doesn't capture as a diff. `Cargo.lock`,
  `build.rs`, `rust-toolchain(.toml)`, and `.cargo/config.toml` do too.
- Building the graph requires `cargo` on `PATH`; it shells out to
  `cargo metadata`, which (like `go list`) may need network access the
  first time it resolves a new dependency.

## Roadmap

This tracks the phased plan in the project design doc:

- **Phase 1 (V1.0)** — Impact-Driven Test Runner (this) + a distributed
  build/dependency cache.
- **Phase 2 (V1.5)** — `fastci analyze`: AI-assisted failure log analysis
  and fix suggestions.
- **Phase 3 (V2.0)** — `fastci local` (fast local CI reproduction) and
  `fastci guard` (supply-chain / runtime security guardrails).

Language coverage grows incrementally alongside this. Candidates being
considered next: Vite `resolve.alias` resolution, Vitest/Jest
monorepo/workspace cross-package resolution, and function-level (not just
package/crate/file-level) impact analysis.

## License

MIT — see [LICENSE](LICENSE).
