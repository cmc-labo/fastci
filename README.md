# fastci

**fastci** is a lightweight CI accelerator. Its first feature is the
**Impact-Driven Test Runner**: it looks at your git diff, builds a
dependency graph for your project, and runs your tests only against the
packages/files that actually changed or transitively depend on something
that changed — instead of your whole test suite.

No Bazel, no build-system migration. Drop it in front of your test runner
in your existing GitHub Actions workflow (or run it locally) and it gets
out of the way otherwise.

![fastci narrowing a one-file change to 3 of 43 Go test targets, explaining why one of them was selected with --why, and previewing a run with --dry-run](docs/demo.gif)

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
   skipping something that matters. A change to a widely-depended-on file
   (a shared core library, say) is already covered by this same graph walk:
   every test that transitively depends on it is selected, precisely,
   without any special-casing. `--full-run-threshold` (see [Usage](#usage))
   adds an optional, separate safety net for the opposite situation - a
   diff broad enough that per-file narrowing itself carries more risk than
   it saves.

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

# Safety net: run everything anyway if a diff this broad touched >=30% of
# tracked source files, rather than trusting per-file narrowing on it
fastci test --full-run-threshold 30

# Explain why a file/package/crate was (or wasn't) selected - diagnostic
# only, doesn't run any tests
fastci test --why src/consumer.test.ts
fastci test --why internal/impact          # a Go import path works too

# Force a real run of everything selected, bypassing the local
# test-result cache (see below)
fastci test --no-cache

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

An unmarked line's exact dependency chain, or why some file *isn't* in the
list at all, can be checked directly with `--why`:

```
$ fastci test --why src/consumer.test.ts
fastci: why is "src/consumer.test.ts" selected?
  Selected because of this dependency chain:
    src/leaf.ts (changed)
    -> src/mid.ts
    -> src/consumer.ts
    -> src/consumer.test.ts

$ fastci test --why src/isolated.test.ts
fastci: why is "src/isolated.test.ts" selected?
  NOT selected: no changed file's effect reaches this target through the dependency graph.
```

### Test-result cache (local and distributed)

Every selected target is checked against a content-hash-keyed cache
(`.fastci-cache/test-results.json`, git-ignored automatically) before it's
actually run: if the target's own files and everything it transitively
depends on are byte-for-byte identical to a previous run that passed (under
the same test-runner invocation), it's skipped entirely rather than
re-executed. This is a different, more precise mechanism than the
dependency-graph narrowing `fastci test` already does above — that
narrowing decides *which targets a diff could possibly affect*; this cache
asks *have we already seen this exact content pass at all*, so it keeps
finding things to skip even during a full run (a lockfile change, say,
forces every target to be considered, but most of them likely didn't
actually change content):

```
$ fastci test -v
fastci: could not safely narrow the test set, running the full suite (go). Reason(s):
  - go.mod
fastci: 3/3 target(s) skipped (cache hit - unchanged content already passed): pkga, pkgb, pkgc
fastci: nothing to run - every selected target was a cache hit
```

Only a *passing* result is ever trusted — a cached failure never skips a
run, since that would hide a real problem instead of saving time. A target
whose dependency graph includes an unresolvable dynamic import (marked `~`
in the [output legend](#usage) above) is never cached either, for the same
reason it's always treated as possibly affected there: the graph can't
prove it has captured that target's full dependency set. `--no-cache`
bypasses this entirely and always actually runs every selected target.

By default this is a local-machine cache only. Setting
`FASTCI_REMOTE_CACHE_URL` additionally shares it across machines and CI
runners — the "distributed" half of the [Roadmap](#roadmap)'s Phase 1
cache:

```sh
export FASTCI_REMOTE_CACHE_URL=https://cache.example.com/fastci
export FASTCI_REMOTE_CACHE_TOKEN=...   # optional, sent as a bearer token
fastci test
```

The protocol is a minimal HTTP GET/PUT key-value store — the same shape
Bazel's remote cache or sccache use: `GET <url>/<key>` returning 200 means
a hit, 404 (or anything else) means a miss; `PUT <url>/<key>` records a
pass. fastci has no opinion on what's actually behind the URL: a small
self-hosted server, a static-file host that accepts PUT, an S3-compatible
bucket via presigned URLs a CI job generates ahead of time, etc. Every
request has a 5-second timeout and is best-effort — a network error, a
timeout, or the remote being entirely unreachable degrades to "just don't
use the remote cache" (one warning printed to stderr per run, not a failed
build), never blocking or failing the actual test run. A remote hit is
folded into the local cache file too, so a later run on the same machine
doesn't pay for another round trip to see it again.

### `fastci analyze`

When `fastci test` fails, it saves the failing run's combined output to
`.fastci-cache/last-failure.json` (git-ignored automatically, same as the
[pytest AST cache](#current-limitations)). `fastci analyze` reads that and
asks Claude to diagnose it:

```sh
export ANTHROPIC_API_KEY=sk-ant-...
fastci test        # fails
fastci analyze      # asks Claude why, using the failure fastci test just captured
```

```
$ fastci analyze
fastci: asking claude-sonnet-5 about the go failure from 2026-09-08 08:24:44...

Root cause: pkga.A() returns 1 but the test expects 2. Fix: either update
A() to return 2, or fix the test's expected value if 1 is actually correct.
```

If the last `fastci test` run passed (or none has run yet), `analyze` just
says so — there's nothing to diagnose. Options:

```sh
fastci analyze --model claude-opus-5   # a different model
fastci analyze --max-tokens 2048       # allow a longer response
```

**This makes a real, billed network request to `api.anthropic.com`.** It
requires `ANTHROPIC_API_KEY` in the environment, and only ever runs when you
explicitly invoke `fastci analyze` — never automatically as part of
`fastci test`. The captured output sent to Anthropic can include file paths,
source snippets, or other project-specific content from your test run;
don't run it on output you wouldn't want leaving your machine.
(`ANTHROPIC_BASE_URL` is also honored, for a proxy or an API-compatible
alternative endpoint.)

### `fastci guard`

Phase 3's `fastci guard` (see [Roadmap](#roadmap)): supply-chain security
scanning. Most of its checks run each ecosystem's own official, trusted
scanner and report what it finds, rather than implementing vulnerability
detection itself:

| Ecosystem | Scanner | Install |
| --- | --- | --- |
| Go | [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | `go install golang.org/x/vuln/cmd/govulncheck@latest` |
| JS/TS | `npm audit` / `pnpm audit` / `yarn audit` (picked by lockfile) | comes with Node.js |
| Python | [`pip-audit`](https://pypi.org/project/pip-audit/) | `pip install pip-audit` |
| Rust | [`cargo-audit`](https://github.com/rustsec/rustsec) | `cargo install cargo-audit` |

One check is fastci's own, since no equivalent official tool exists for it:
for JS/TS projects with a `node_modules` present, it scans every installed
dependency's `package.json` for a `preinstall`/`install`/`postinstall`
script - the mechanism behind real supply-chain attacks like event-stream
(2018) and ua-parser-js (2021), where malicious code ran automatically the
moment a dependency was installed. A lifecycle script isn't inherently
malicious (esbuild, puppeteer, and husky all legitimately use one), so this
doesn't judge intent - it just surfaces which dependencies can run code at
install time, so a human can look.

```sh
fastci guard
```

Unlike `fastci test`, which picks a single project type, `guard` detects
and runs *every* applicable check independently - a monorepo with both a
`go.mod` and a `package.json` gets govulncheck, js audit, and the lifecycle
script scan. A check whose underlying tool isn't installed (or, for the
lifecycle scan, whose `node_modules` doesn't exist yet) is skipped with an
install hint printed, rather than failing the whole command; `guard` exits
non-zero only when a check that did run reports something.

`guard`'s other, runtime piece is a `--network-report` flag on `fastci
test` (and `fastci local`, below) rather than its own subcommand, since
it has to wrap the actual test/build process running - something only
those two already do:

```sh
fastci test --network-report
```

```
$ fastci test --network-report
ok  	example.com/netcheck	0.191s
fastci: network report: 1 host(s) contacted:
  example.com:80
```

It points the test/build run at a small local proxy (via
`HTTP_PROXY`/`HTTPS_PROXY`, restored to whatever they were before once the
run finishes) and reports every host it contacted - useful for noticing a
dependency phoning home somewhere unexpected during a build or test run.
HTTPS traffic is tunneled through the proxy unmodified: it only ever reads
the plaintext `CONNECT host:port` line itself to learn the destination, and
never holds a TLS certificate/key that would let it decrypt or inspect
anything past that. It's purely informational - it doesn't block or fail
the run based on what it sees, unlike the allowlist-enforcement approach
some tools take, which this deliberately doesn't do.

### `fastci local`

Phase 3's other piece (see [Roadmap](#roadmap)): reproducing what CI would
run, locally, without having to remember or hand-pick `--base`. `local`
reads `.github/workflows/*.yml` for the base branch a pull request would be
diffed against, then runs `fastci test` with that as `--base`:

```sh
fastci local
```

It looks for the base branch in this order:

1. An explicit `on.pull_request.branches` in a workflow file.
2. Failing that, the same workflow's `on.push.branches` - a bare
   `pull_request:` trigger with no branches filter is common (GitHub
   already scopes it to the PR's own base branch, so there's often nothing
   to read there), and a repo's main integration branch is almost always
   both what pushes deploy from and what pull requests target.
3. Failing that, the repository's actual default branch
   (`refs/remotes/origin/HEAD`).
4. As a last resort, `main`.

```
$ fastci local
fastci: reproducing CI locally against origin/main (no pull_request.branches filter found, inferred from on.push.branches in .github/workflows/ci.yml)
fastci: 2 changed file(s):
  ...
```

It accepts the same `--dry-run`, `--no-cache`, and `-- <flags>` passthrough
as `fastci test` (see above); `--base` isn't accepted, since detecting it
is the entire point.

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
          fetch-depth: 0   # simplest: full history, no recovery fetch needed at all

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

      - run: go install github.com/hpscript/fastci/cmd/fastci@latest
      - run: fastci test --base origin/${{ github.base_ref }}
```

`fetch-depth: 0` above is a recommendation, not a hard requirement: if you leave the
default `fetch-depth: 1` (or any other shallow/partial checkout) in place,
`fastci test --base <ref>` detects that it can't resolve a merge base and
automatically runs the equivalent of `git fetch --unshallow` and/or fetches
`<ref>` itself before diffing — the same recovery you'd otherwise have to
script by hand. This only kicks in when needed, so it costs nothing on a
full checkout; it does mean the first `fastci test` invocation on a shallow
checkout does extra network I/O. If that fetch itself fails (no network, a
`<ref>` that doesn't exist at all, permissions), fastci reports a clear
error rather than a bare `git` failure.

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
- AST parsing is cached per file in `<project>/.fastci-cache/pytest-imports.json`,
  keyed by each file's mtime and size — a file whose (mtime, size) hasn't
  changed since the cache was written reuses its previous result instead of
  being re-parsed. Adding, removing, or renaming any `.py` file anywhere in
  the project invalidates the whole cache for that run (a full reparse,
  same as no cache at all) rather than risk reusing a resolution a changed
  registry could have made stale. This is a fast check, not a content
  hash: a file edited twice within the same mtime tick that also happens to
  land on the exact same byte size (rare) could be missed — delete
  `.fastci-cache/` to force a full reparse if that's ever a concern. The
  cache directory is git-ignored automatically (it writes its own
  `.gitignore`), so nothing needs to be done to keep it out of version
  control.
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
  build/dependency cache. Both are implemented: the test runner, and the
  cache's local-machine caching plus its distributed, opt-in
  `FASTCI_REMOTE_CACHE_URL`-backed sharing across machines/CI runners —
  see [Test-result cache (local and distributed)](#test-result-cache-local-and-distributed)
  above.
- **Phase 2 (V1.5)** — `fastci analyze`: AI-assisted failure log analysis
  and fix suggestions. Implemented — see [Usage](#usage) and
  [`fastci analyze`](#fastci-analyze) below.
- **Phase 3 (V2.0)** — `fastci local` (fast local CI reproduction) and
  `fastci guard` (supply-chain / runtime security guardrails). Implemented:
  `fastci guard`'s supply-chain vulnerability scanning (govulncheck,
  npm/pnpm/yarn audit, pip-audit, cargo-audit), a JS/TS install-time
  lifecycle-script scan, and a `--network-report` runtime guardrail on
  `fastci test`/`fastci local` that reports which hosts a run contacted —
  see [`fastci guard`](#fastci-guard) above. `fastci local` is implemented
  for its core scope — auto-detecting CI's diff base branch from
  `.github/workflows/*.yml` and running `fastci test` against it locally —
  see [`fastci local`](#fastci-local) above; faithfully replaying a
  workflow's other steps (e.g. its `uses:` actions) is out of scope for
  this. This closes out every item originally planned for Phase 3.

Language coverage grows incrementally alongside this. Candidates being
considered next: Vite `resolve.alias` resolution, Vitest/Jest
monorepo/workspace cross-package resolution, and function-level (not just
package/crate/file-level) impact analysis.

## License

MIT — see [LICENSE](LICENSE).
