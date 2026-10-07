# fastci

**fastci** is a lightweight CI accelerator. Its first feature is the
**Impact-Driven Test Runner**: it looks at your git diff, builds a
dependency graph for your project, and runs your tests only against the
packages/files that actually changed or transitively depend on something
that changed — instead of your whole test suite.

No Bazel, no build-system migration. Drop it in front of your test runner
in your existing GitHub Actions workflow (or run it locally) and it gets
out of the way otherwise. See [Benchmarks](docs/benchmarks/README.md) for
real, reproducible measurements — including where it helps, where it
doesn't, and the scripts to check both on your own hardware — and
[fastci vs. Nx/Turborepo](docs/comparison.md) for how it differs from
monorepo build orchestrators, and when each fits. JS/TS monorepo users
specifically: see
[fastci and your JS/TS monorepo](docs/jsts-monorepo.md) for what
workspace cross-package resolution does and a reproducible demo.

![fastci narrowing a one-file change to 3 of 43 Go test targets, explaining why one of them was selected with --why, and previewing a run with --dry-run](docs/demo.gif)

This is an early, incrementally-developed project. Today it covers:

- **Go** (`go test`) — package-level, module or [workspace](https://go.dev/ref/mod#workspaces)
- **TypeScript/JavaScript with Vitest** — file-level
- **TypeScript/JavaScript with Jest** — file-level
- **Python with pytest** — file-level
- **Rust with Cargo** — crate-level, single crate or [workspace](https://doc.rust-lang.org/cargo/reference/workspaces.html)

See [Roadmap](#roadmap) for what's next.

## Table of contents

- [Quickstart](#quickstart)
- [How it works](#how-it-works)
- [Usage](#usage)
  - [Test-result cache (local and distributed)](#test-result-cache-local-and-distributed)
  - [`fastci analyze`](#fastci-analyze)
  - [`fastci guard`](#fastci-guard)
    - [Distinguishing a real finding from an infrastructure failure](#distinguishing-a-real-finding-from-an-infrastructure-failure)
    - [PipAudit (Python)](#pipaudit-python)
    - [LifecycleScripts (JS/TS)](#lifecyclescripts-jsts)
    - [CargoBuildScripts (Rust)](#cargobuildscripts-rust)
    - [Network egress guardrail (`--network-report`)](#network-egress-guardrail---network-report)
  - [`fastci local`](#fastci-local)
  - [Function-level impact analysis (Go)](#function-level-impact-analysis-go)
- [GitHub Actions](#github-actions)
- [Current limitations](#current-limitations)
- [Troubleshooting](#troubleshooting)
- [Roadmap](#roadmap)
- [License](#license)

## Quickstart

### 1. Install

fastci itself is a single Go binary, installed with `go install` — this is
required even if the project you'll run it *against* is JavaScript, Python,
or Rust; only building fastci needs a Go toolchain, not your project:

```sh
go install github.com/hpscript/fastci/cmd/fastci@latest
```

This needs Go on your machine ([install Go](https://go.dev/doc/install) if
you don't have it) — Go 1.21 or later is enough, since `GOTOOLCHAIN=auto`
(the default since Go 1.21) transparently downloads whatever newer toolchain
fastci's own `go.mod` requires.

Verify it installed and is on your `PATH`:

```sh
fastci --help
```

If that prints `command not found: fastci`, `go install` put the binary in
`$(go env GOPATH)/bin`, which isn't on `PATH` by default on many systems:

```sh
export PATH="$PATH:$(go env GOPATH)/bin"
```

Add that line to your shell profile (`~/.bashrc`, `~/.zshrc`, etc.) to make
it permanent.

### 2. Run it against your own project

`cd` into your project's root — wherever its `go.mod`/`go.work`,
`vitest.config.*`, Jest-configured `package.json`, pytest config, or
`Cargo.toml` lives (see [How it works](#how-it-works) for exactly what's
auto-detected) — and first preview what fastci would do, without running
anything:

```sh
fastci test --dry-run -v
```

On a clean working tree this reports `fastci: no changed files detected,
nothing to test` — that's expected, since fastci narrows based on your git
diff, not on the whole project. Make a small, real edit to one file (or
check out a branch that already has one), then try again. You should see
the file(s) you changed and the test targets fastci selected because of
them. Once you're satisfied it picked the right thing, drop `--dry-run` to
actually run them:

```sh
fastci test
```

### 3. Try a PR-style diff

The commands above compare your working tree against `HEAD` — uncommitted
changes. To preview what a pull request would trigger instead (committed
changes against a target branch, the way CI sees it):

```sh
fastci test --base origin/main
```

### 4. Wire it into CI

See [GitHub Actions](#github-actions) below for a drop-in workflow step that
runs this automatically on every pull request.

That's the core loop. Everything else in this README is reference
material: the full [flag list and output format per language](#usage), the
[test-result cache](#test-result-cache-local-and-distributed), [`fastci
analyze`](#fastci-analyze) (AI-assisted failure triage), [`fastci
guard`](#fastci-guard) (vulnerability scanning), [`fastci
local`](#fastci-local) (reproduce CI locally), and
[per-language limitations](#current-limitations). If something doesn't
behave as expected, check [Troubleshooting](#troubleshooting) first.

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
     `tsconfig.json`) are tracked too. Vitest's own `resolve.alias`
     (`vite.config.*`/`vitest.config.*`) is resolved the same way Vite
     itself resolves it — by actually bundling and executing the config
     file under Node — since, unlike `moduleNameMapper`, it's arbitrary
     JS/TS code rather than a JSON-shaped value; see
     [Current limitations](#current-limitations) for what that needs. A
     cross-package import within an npm/pnpm/yarn workspace monorepo
     (e.g. `import {x} from '@myorg/utils'` from a sibling package) is
     resolved for both Vitest and Jest the same way — see
     [Current limitations](#current-limitations) below.
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
build), never blocking or failing the actual test run. A circuit breaker
additionally stops attempting the remote at all for the rest of the run
after a few consecutive failed round trips, so a remote that's merely slow
to fail (rather than refusing the connection outright) can't turn into a
real multi-minute stall across a large target set, one 5-second timeout at
a time - see
[docs/remote-cache-circuit-breaker.md](docs/remote-cache-circuit-breaker.md)
for exactly how that's decided and what it changes for you. A remote hit
is folded into the local cache file too, so a later run on the same
machine doesn't pay for another round trip to see it
again.

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
detection itself. This is the full reference; for a hands-on, worked
walkthrough of every feature (install steps, a vulnerable-dependency
fix-it example, CI setup), see
[docs/guard-guide.md](docs/guard-guide.md).

| Ecosystem | Scanner | Install |
| --- | --- | --- |
| Go | [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) | `go install golang.org/x/vuln/cmd/govulncheck@latest` |
| JS/TS | `npm audit` / `pnpm audit` / `yarn audit` (picked by lockfile) | comes with Node.js |
| Python | [`pip-audit`](https://pypi.org/project/pip-audit/) | `pip install pip-audit` |
| Rust | [`cargo-audit`](https://github.com/rustsec/rustsec) | `cargo install cargo-audit` |

#### Distinguishing a real finding from an infrastructure failure

**The problem** — every scanner above can fail to even complete its scan,
most commonly from having no network access to fetch its own
vulnerability/advisory database (a flaky connection, a firewalled CI
runner, a dead proxy). Several of them - npm/pnpm/yarn audit, pip-audit,
and cargo-audit - reuse the *exact same* exit code for that as they do for
"the scan completed and found a real vulnerability." Treating any
non-zero exit as a finding, the naive generic-Unix-tool assumption, would
misreport an ordinary network hiccup as a real vulnerability - this was a
genuine bug found in this project (by deliberately forcing every scanner
to fail this way against the real tool, not just by reading their docs)
and fixed per-tool, as described below.

**How each tool is told apart**:

- **govulncheck** doesn't need any text matching: its own docs reserve a
  specific exit code, `3`, exclusively for "your code is affected by a
  vulnerability it can actually reach." Exit `0` is clean; any other exit
  code (most commonly `1`, from a database-fetch failure) is treated as
  the tool failing to complete, never as a finding.
- **npm, pnpm, and yarn audit** reuse the same exit code for both outcomes,
  so fastci inspects the tool's own output text instead, looking for any
  of: npm's `audit endpoint returned an error`; pnpm's
  `ERR_PNPM_AUDIT_BAD_RESPONSE`; or, as a broader net covering all three
  (yarn in particular has no distinct string of its own - its real failure
  text is a generic connection-error dump) - a Node.js network error code
  (`ECONNREFUSED`, `ENOTFOUND`, `ETIMEDOUT`, `ECONNRESET`) or the
  plain-English OS errno text pnpm's own Rust-based HTTP client prints
  instead (`Connection refused`, `Connection reset`, `Connection timed
  out`).
- **pip-audit** likewise reuses the same exit code; fastci recognizes an
  unhandled Python traceback (`Traceback (most recent call last):`) or one
  of pip-audit's own logged `ERROR:pip_audit.*` messages, neither of which
  its real vulnerability-report format (a formatted table) could ever
  produce.
- **cargo-audit** also reuses the same exit code; fastci looks for its own
  specific message, `couldn't fetch advisory database`.

Every one of these markers was verified directly against the real tool,
not inferred from documentation: by pointing `HTTP_PROXY`/`HTTPS_PROXY` at
a port nothing listens on and running the real scanner against it, then
capturing its actual failure text - the same empirical approach used
throughout this project.

**How it's reported** — each checker's `Run` has exactly three possible
outcomes:

| Outcome | What's printed | Counts as a "finding"? |
| --- | --- | --- |
| Real vulnerability found | the tool's own output, then `fastci: <name> reported issues (see above)` | yes - `guard` exits non-zero |
| Clean scan, nothing found | `fastci: <name>: no issues found` | no |
| Infrastructure failure | `fastci: <name>: could not complete - <reason>` | no - treated exactly like the tool not being installed at all ("couldn't check", not "checked and found a problem") |

Two real, captured examples - the same project, with and without a live
network connection:

```
$ HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 fastci guard
fastci: running govulncheck...
fastci: govulncheck: could not complete - govulncheck: exited 1 without completing the scan (a usage error, or most commonly a failure to fetch its vulnerability database - not a vulnerability finding, which uses exit code 3 specifically):
govulncheck: fetching vulnerabilities: Get "https://vuln.go.dev/index/modules.json.gz": proxyconnect tcp: dial tcp 127.0.0.1:1: connect: connection refused

fastci: no vulnerabilities found by the scanners that ran, but at least one applicable scanner was skipped - see above
```

```
$ fastci guard
fastci: running govulncheck...
=== Symbol Results ===

Vulnerability #1: GO-2021-0113
    Out-of-bounds read in golang.org/x/text/language
  More info: https://pkg.go.dev/vuln/GO-2021-0113
  Module: golang.org/x/text
    Found in: golang.org/x/text@v0.3.0
    Fixed in: golang.org/x/text@v0.3.7
    Example traces found:
      #1: main.go:6:23: m.main calls language.Parse

Your code is affected by 1 vulnerability from 1 module.

fastci: govulncheck reported issues (see above)

fastci: one or more scanners reported vulnerabilities
```

A checker that "could not complete" never stops any other applicable
checker in the same run from still executing - a monorepo with both a
`go.mod` and a `package.json` still gets its npm audit run even if
govulncheck's own database fetch failed. Each check also runs in its own
process group with a bounded timeout, so a single scanner that hangs
outright can't block the rest of `guard` either - see below.
Across the whole `guard` run: any real finding makes it exit non-zero
regardless of anything else; with no finding but at least one checker that
couldn't complete or was skipped, it exits `0` but says so explicitly
rather than silently reporting a clean bill of health; only when every
applicable checker both ran and found nothing does it report a plain "no
vulnerabilities found".

#### PipAudit (Python)

**Detection** — this check becomes applicable whenever the working
directory contains a `requirements.txt`, `pyproject.toml`, `Pipfile`,
`setup.py`, or `setup.cfg`; otherwise it's skipped entirely, the same as
any inapplicable checker (not reported as a failure).

**What it audits**:
- If a `requirements.txt` is present, it's audited directly
  (`pip-audit -r requirements.txt`) — the project's own dependencies don't
  need to be installed first, the same no-install-required approach
  fastci's own pytest analyzer already uses to build its import graph.
- Otherwise (e.g. a `pyproject.toml`-only project, or one with no
  `requirements.txt` committed), it falls back to pip-audit's own default
  behavior: auditing every package already installed in the active Python
  environment.

**Configuration** — there's no fastci-specific config file or flag for
this; the one thing worth understanding is how the active-environment
fallback targets the right interpreter. By default, pip-audit resolves
installed packages against whatever Python interpreter *it itself* happens
to be installed under (`sys.executable`), not necessarily the project's
active virtualenv — a pip-audit installed globally or via `pipx` would
otherwise silently audit a completely unrelated environment (pip-audit
even warns about this itself: "This may result in unintuitive audits").
To avoid that, this checker points pip-audit at whatever `python3` resolves
to on `PATH`, via pip-audit's own `PIPAPI_PYTHON_LOCATION` environment
variable. Practically, that means: **activate the project's virtualenv (or
otherwise make sure its `python3` is first on `PATH`) before running
`fastci guard`** when relying on this fallback — a `requirements.txt`
doesn't need this, since it's audited directly without touching any
environment.

**Vulnerabilities detected** — pip-audit queries PyPI's own vulnerability
data by default (its `pypi` vulnerability service, no API key or extra
setup needed), backed by the
[PyPA Advisory Database](https://github.com/pypa/advisory-database)/[OSV.dev](https://osv.dev)'s
Python advisories. Each finding reports the affected package and installed
version, the advisory ID (`PYSEC-...`, often with a cross-referenced
`CVE-...`/`GHSA-...` alias), and the fixed version(s) to upgrade to — the
same information running `pip-audit` directly would print. This covers
both direct and transitive dependencies actually resolved from the
requirements file or environment; unlike govulncheck for Go, it does not
do reachability analysis — a vulnerable package merely being installed is
reported regardless of whether your code actually calls the vulnerable
code path.

**Real finding vs. an infrastructure failure** — like the other scanners
`fastci guard` wraps, pip-audit reuses the same exit code both for "found
vulnerabilities" and for failing to even complete the audit (most commonly
no network access to fetch vulnerability data). This checker tells the two
apart from pip-audit's own output: an unhandled Python traceback, or one of
its own logged `ERROR:pip_audit.*` messages, is treated as "could not
complete" rather than a finding — see the exit-code note below for why
this distinction matters in general.

Two checks are fastci's own, since no equivalent official tool exists for
either:

- For JS/TS projects with a `node_modules` present, it scans every
  installed dependency's `package.json` for a `preinstall`/`install`/
  `postinstall` script - the mechanism behind real supply-chain attacks
  like event-stream (2018) and ua-parser-js (2021), where malicious code
  ran automatically the moment a dependency was installed. A lifecycle
  script isn't inherently malicious (esbuild, puppeteer, and husky all
  legitimately use one), so this doesn't judge intent - it just surfaces
  which dependencies can run code at install time, so a human can look.
- For Rust projects, it runs `cargo metadata` and flags every dependency
  (direct or transitive) that defines a custom build script - the same
  class of risk as a JS lifecycle script, but at *build* time instead of
  install time: a `build.rs` (or whatever Cargo.toml's own `build` field
  names it - detected via `cargo metadata`'s own structured output, not a
  filename guess) runs arbitrary Rust code before the crate's own code is
  even compiled. Same non-judgmental framing as the lifecycle-script scan:
  linking a C library or emitting `cfg` flags are common, legitimate uses.

#### LifecycleScripts (JS/TS)

**Detection** — this check becomes applicable whenever the working
directory contains a `package.json`. It additionally requires a
`node_modules` directory to actually scan; if one doesn't exist yet (no
`npm install`/`pnpm install`/`yarn install` has been run), it's skipped
with that install hint printed, the same as a missing scanner binary for
any of the other checks — not reported as a failure.

**What it scans** — it walks every `package.json` under `node_modules`
(including scoped packages like `@org/name`, and every level of nested,
transitive `node_modules`) and reads each one's own `scripts` field
looking for a `preinstall`, `install`, or `postinstall` entry. This is a
pure, read-only filesystem scan: it never runs `npm install` (or any
install command) itself and never executes any script it finds — fastci
shouldn't trigger arbitrary code execution as a side effect of running a
security scan. It only reports on dependencies **already installed on
disk** at scan time, so results reflect whatever `node_modules` currently
contains; reinstalling or updating dependencies and re-running `fastci
guard` picks up any change.

**Configuration** — there's nothing to configure: no flags, no ignore
list, no config file. It isn't meant to replace judgment, so it doesn't
try to decide which scripts are suspicious — see "risk" below.

**Risk this surfaces** — an install-time lifecycle script is real,
long-standing attack surface in the npm ecosystem: it's the exact
mechanism behind supply-chain attacks like event-stream (2018) and
ua-parser-js (2021), where malicious code ran automatically the moment a
compromised package was installed, before any application code ever
executed and often before a human ever looked at what was pulled in. A
lifecycle script is **not inherently malicious** — plenty of legitimate,
widely-used packages (esbuild, puppeteer, husky, core-js, and others) use
one to fetch a prebuilt binary, compile a native addon, or set up git
hooks — so this check deliberately doesn't try to judge intent or flag
specific packages as bad. It only surfaces *which* installed dependencies
can run arbitrary code automatically at install time, so a human can
decide whether each one is expected.

Example output:

```
$ fastci guard
fastci: running npm/pnpm/yarn lifecycle scripts...
2 installed dependencies define an install-time lifecycle script - not necessarily malicious, but each one runs code automatically during install, so review any you don't recognize:
  @org/native-thing@0.1.0: preinstall
  sketchy-pkg@2.1.0: postinstall
fastci: npm/pnpm/yarn lifecycle scripts reported issues (see above)
```

A clean result (no installed dependency defines any of the three scripts)
reports `no installed dependency defines a preinstall/install/postinstall
script` and doesn't count as an issue.

#### CargoBuildScripts (Rust)

**Detection** — this check becomes applicable whenever the working
directory contains a `Cargo.toml`. It requires `cargo` on `PATH` to run
(not `cargo-audit` — plain `cargo`, already required to build the project
at all); if `cargo` isn't found, it's skipped with that install hint
printed, the same as a missing scanner binary for any other check — not
reported as a failure.

**What it scans** — it runs `cargo metadata --format-version=1` and reads
its structured JSON output for every package's own build targets, flagging
any **dependency** (direct or transitive) whose target list includes one
of kind `"custom-build"` — Cargo's own resolution of whatever `Cargo.toml`'s
`build` field actually names (conventionally `build.rs`, but not
necessarily), not a filename guess. Only *dependencies* are in scope, the
same way [LifecycleScripts](#lifecyclescripts-jsts) only scans
`node_modules`: a project's own build script is something its own
developers already wrote and can already see, not a third-party
supply-chain risk, so the project's own root crate (and every other member
of its own `[workspace]`, if any) is never flagged, no matter what its own
`build` field says. Because `cargo metadata` resolves the real dependency
graph the same way `cargo build`/`cargo test` would, this needs network
access the first time it resolves a new dependency, same as an ordinary
build.

**Configuration** — there's nothing to configure: no flags, no ignore
list, no config file, matching LifecycleScripts' same deliberately
judgment-free design.

**Risk this surfaces** — a build script (`build.rs`) runs arbitrary Rust
code automatically at *build* time, before the crate's own code is even
compiled or used — the same class of supply-chain risk as a JS/TS
install-time lifecycle script (see
[LifecycleScripts](#lifecyclescripts-jsts)), just triggered by `cargo
build`/`cargo test` instead of a package-manager install step, and real
malicious-crate incidents have used exactly this mechanism. A build script
is **not inherently malicious** — linking a system C library, generating
code from a schema, or emitting `cfg` flags for conditional compilation
are all common, legitimate uses — so, like LifecycleScripts, this
deliberately doesn't try to judge intent. It only surfaces *which*
dependency crates can run code at build time, so a human can decide
whether each one is expected.

Example output:

```
$ fastci guard
fastci: running cargo build scripts...
1 dependency defines a custom build script - not necessarily malicious, but each one runs arbitrary code automatically at build time, so review any you don't recognize:
  dep@0.1.0: /path/to/dep/build.rs
fastci: cargo build scripts reported issues (see above)
```

A clean result (no dependency defines a custom build script) reports `no
dependency defines a custom build script` and doesn't count as an issue.

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

Each scanner's own exit code is interpreted according to *that tool's*
actual convention, not a generic "any non-zero exit means a finding"
assumption - a check that fails to even complete its scan (most commonly a
network problem fetching its vulnerability/advisory database) is reported
as "could not complete", the same as a missing binary, rather than as a
finding, and - like a missing binary - never stops `guard` from still
running every other applicable check. See
[Distinguishing a real finding from an infrastructure failure](#distinguishing-a-real-finding-from-an-infrastructure-failure)
above for exactly how each tool is told apart, and for real, captured
examples of both outcomes.

Each check also runs in its own process group with a bounded timeout (a
few minutes - generous for an ordinary scan, including a fresh advisory-
database fetch), so a single scanner that hangs outright can't block the
rest of `guard`, or the CI job running it, forever - found to matter in
practice, not just in theory, since some of these tools do spawn their
own child processes that don't always exit cleanly on a failure.

`guard`'s other, runtime piece is a `--network-report` flag on `fastci
test` (and `fastci local`, below) rather than its own subcommand, since
it has to wrap the actual test/build process running - something only
those two already do.

#### Network egress guardrail (`--network-report`)

**Activation** — unlike the Checkers above, this isn't auto-detected; it's
an opt-in flag you pass explicitly, since it changes how the test/build
run itself executes rather than running an independent scan alongside it:

```sh
fastci test --network-report
fastci local --network-report   # same flag, same behavior
```

**What it does** — it starts a small local forward proxy on an ephemeral
port and points the test/build run at it by setting `HTTP_PROXY`,
`HTTPS_PROXY`, and their lowercase variants (`http_proxy`/`https_proxy` -
different tools disagree on which casing they honor, so both are set) for
the duration of that one run only; whatever those variables held before
(a real corporate proxy, say, or nothing at all) is restored exactly once
the run finishes, even if the run itself fails. Every distinct host the
run contacted through the proxy is then reported:

```
$ fastci test --network-report
ok  	example.com/netcheck	0.191s
fastci: network report: 1 host(s) contacted:
  example.com:80
```

```
$ fastci test --network-report
ok  	example.com/netcheck	0.191s
fastci: network report: no outbound connections observed
```

This is useful for noticing a dependency phoning home somewhere
unexpected during a build or test run - a postinstall/build script (see
[LifecycleScripts](#lifecyclescripts-jsts)/[CargoBuildScripts](#cargobuildscripts-rust)
above) reaching out to a host that has nothing to do with the package
registry, for instance.

**What it can see** — HTTPS traffic (the overwhelming majority of real
traffic: virtually every package registry and API fastci-driven tooling
talks to is HTTPS) is tunneled through the proxy unmodified: it only ever
reads the plaintext `CONNECT host:port` line itself to learn the
destination, then splices the raw TCP connection through byte-for-byte -
it never holds a TLS certificate/key that would let it decrypt or inspect
anything past that line. Plain HTTP requests are forwarded the same way an
ordinary HTTP forward proxy does, which does mean their request line and
headers pass through in the clear (rare in practice, for the reason
above).

**Configuration and limitations** — there's no port, host, or allowlist to
configure; it's purely informational and never blocks or fails the run
based on what it sees, unlike the allowlist-enforcement approach some
tools take, which this deliberately doesn't do. The one real limitation
worth knowing: it can only see traffic from a tool that actually honors
`HTTP_PROXY`/`HTTPS_PROXY` (or the lowercase variants) in the first
place - nearly all HTTP(S) clients in every ecosystem fastci targets do,
but a tool making a raw TCP/UDP connection, resolving DNS itself and
dialing an IP directly, or otherwise ignoring the proxy environment
variables entirely, would make a real outbound connection that never
passes through the proxy and so never appears in the report.

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

### Function-level impact analysis (Go)

Everything above narrows *which packages* to test. For Go, `fastci test`
additionally tries to narrow *which tests within a selected package* —
down from every test in it to just the ones that actually, transitively
call whatever function changed:

```
$ fastci test -- -v
fastci: selected 1/1 test target(s) (go, 0% skipped)
  * example.com/calc
=== RUN   TestComputeViaAdd
--- PASS: TestComputeViaAdd (0.00s)
PASS
ok  	example.com/calc	0.002s
```

(`TestMultiply`, also in that package, was correctly left out — nothing it
exercises calls the function that actually changed.)

This builds a real call graph for the whole module using
[RTA](https://pkg.go.dev/golang.org/x/tools/go/callgraph/rta) (Rapid Type
Analysis, from every test function in the module as roots — the same
`golang.org/x/tools` family `go/packages` is already part of), then walks
it backward from the changed function to find every transitive caller
among those roots, and passes the result to `go test` as a `-run` pattern.
RTA, not the simpler CHA algorithm, is essential here: CHA resolves any
non-static call by signature alone against *every* function in the whole
program (stdlib included), which for a plain function with a common
signature like `func(int, int) int` reaches close to essentially
everything and makes narrowing useless.

That backward walk only ever follows a **static** call edge — an ordinary
call to a named function or a method on a concrete (non-interface) type.
It deliberately never crosses an interface method call or a plain
function/closure value call, even though RTA resolves those too: RTA
resolves a dynamic call site's possible callees per *call site*, not per
calling instance, so if two unrelated callers each pass their own closure
(or their own concrete type, for an interface call) through the very same
higher-order function or interface method — an everyday pattern,
`testing.T.Run` itself being the most common example, since every subtest
passes its own closure through it — RTA can't tell the two apart. In a
codebase using `t.Run` at all (nearly all real Go test suites do), that
turns into "every test is reachable from every other test" within a
handful of hops, discovered the hard way by running this against fastci's
own test suite during development. Restricting to static edges gives up on
narrowing through an interface or closure boundary (that part of the diff
falls back to running everything, the same as any other case this can't
safely narrow) in exchange for the results that *do* come back being
genuinely trustworthy rather than an unbounded, often near-total
over-approximation.

One direct consequence: if a diff's selected packages include one whose
only real path to the changed function crosses such a boundary, this
doesn't narrow *that* package down to an empty, wrong test set — it
recognizes the gap and falls back to running every test in every selected
package instead, exactly as if narrowing weren't attempted at all. So a
codebase whose packages mostly talk to each other through interfaces (a
perfectly normal, often desirable design — fastci's own analyzer
abstraction is one) will see this narrow less often across package
boundaries, but never incorrectly.

This is deliberately conservative and always all-or-nothing for the whole
diff: it only ever narrows when *every* changed file is a non-test `.go`
file whose diff hunks each fall entirely inside one existing function's
body — no added/removed functions, no signature changes, no package-level
declaration changes, no test file changes, nothing unparseable — **and**
every one of the diff's already-selected target packages has at least one
test reachable through a static-only call chain. Failing either makes it
fall straight back to running every test in the normally-selected
packages, same as if this didn't exist; it never narrows a package down to
*zero* tests. An explicit `--run`/`-run` you pass yourself (e.g. `fastci
test -- -run TestFoo`) is never overridden.

Like RTA itself, this can't see calls made via reflection, cgo, assembly,
or `//go:linkname` — a real, if narrow, gap in soundness for code relying
on those, accepted here for a real reduction in what has to run for the
overwhelmingly common case of an ordinary function-body edit reached by an
ordinary, direct call chain.

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

**Jenkins users**: fastci needs no plugin - it runs from a plain shell
step in any job type. See [docs/jenkins.md](docs/jenkins.md) for a full
setup guide: Declarative Pipeline and Freestyle examples, getting
`--base` right for both regular and pull-request builds, shallow clones,
and `fastci analyze`/`fastci guard` as their own stages.

## Current limitations

**Go**
- Run `fastci` from either a Go module root (`go.mod` present) or a Go
  workspace root (`go.work` present, listing member modules as
  subdirectories). Cross-module import edges within a workspace are
  resolved correctly, including test-only edges. `go.work`/`go.work.sum`
  changes trigger a full run, same as `go.mod`/`go.sum`.
- Package-level impact analysis (which packages to test) always applies.
  On top of that, [function-level narrowing](#function-level-impact-analysis-go)
  (which tests *within* a selected package) applies only when the diff is
  cleanly attributable to specific function bodies; see that section for
  exactly when it does and doesn't kick in.
- Non-Go changes (docs, workflow YAML, etc.) are treated as not affecting
  any test package. Go files that reference non-Go inputs at build time
  (e.g. `//go:embed`) aren't tracked yet.

**Vitest**
- Shares its esbuild-based import resolution and dynamic
  `import()`/`require()` handling with Jest (see below) — everything in
  the Jest section below other than the `moduleNameMapper`/`jest.config.*`
  points applies to Vitest too, including npm/pnpm/yarn workspace
  cross-package resolution.
- Vite's own `resolve.alias` config (in `vite.config.*`/`vitest.config.*`,
  vitest.config.* taking priority when both exist, matching Vitest's own
  precedence) **is** resolved — unlike Jest's `moduleNameMapper`, which is
  a JSON-shaped value that can be read as data, a Vite alias list lives
  inside arbitrary JS/TS config code, so fastci bundles the config file
  with esbuild and actually executes it under Node, the same way
  Vite/Vitest themselves load it, then feeds the resulting alias map into
  esbuild's resolver. This needs `node` on `PATH` and the config's own
  imports (typically just `vite`, for `defineConfig`) already installed in
  `node_modules`; without either, or if the config fails to execute for any
  other reason, this degrades silently back to the old behavior — an
  alias-only-reachable import is invisible to the graph — rather than
  failing the whole analysis. `resolve.alias` entries keyed by a `RegExp`
  (Vite allows this; esbuild's own alias resolver only supports
  string/prefix matching) are skipped the same way. `tsconfig.json`
  `paths`/`baseUrl` aliases (which esbuild resolves directly, with no
  execution needed) are unaffected by any of this and always work.
- Test-file discovery uses Vitest's default `include` pattern
  (`**/*.{test,spec}.?(c|m)[jt]sx?`). A custom `test.include`/`test.exclude`
  in `vitest.config.*` isn't honored yet — such a project still works, but
  test-file classification falls back to the default.
- Any `vitest.config.*` or `vite.config.*` change forces a full run (same
  treatment as `jest.config.*` for Jest), since either can change aliases,
  plugins, or test settings the import graph can't see.

**Jest**
- Bare specifiers that resolve into `node_modules` are treated as external
  and are not walked further, **except** a cross-package import within an
  npm/pnpm/yarn **workspace monorepo** — e.g. `import {x} from
  '@myorg/utils'` from a sibling package — which *is* resolved to that
  package's real files (both its bare main entry and any subpath, e.g.
  `@myorg/utils/helpers`). This is fully static: the workspace root's
  `package.json` `"workspaces"` field (npm/yarn - either the array or
  `{"packages": [...]}` form) or `pnpm-workspace.yaml`'s `packages:` list
  (pnpm) is read to map each member package's name to its directory, then
  esbuild's own resolver is pointed at that directory - `npm
  install`/`pnpm install`/`yarn install` having been run is not required.
  fastci must be run from the workspace root for this (the same place that
  manifest lives) — running it from within a single member package treats
  that package in isolation, with cross-package imports staying external
  as before. An exclusion glob (a pattern starting with `!` in
  `pnpm-workspace.yaml`) is not honored; a member matched by a broader
  inclusion pattern despite an unread exclusion is only ever a false
  *inclusion*, never a missed one.
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

## Troubleshooting

**`command not found: fastci` after `go install`**
`go install` places the binary in `$(go env GOPATH)/bin`, which isn't on
`PATH` by default on many systems. Add it:
`export PATH="$PATH:$(go env GOPATH)/bin"` — see
[Quickstart](#quickstart).

**`fastci: no supported project detected in <dir>`**
fastci auto-detects the project type by looking for one of: a Go
`go.mod`/`go.work`, a Vitest config or `vitest` dependency, a
Jest-configured `package.json`, a pytest config (`pytest.ini`,
`conftest.py`, or a `[tool.pytest.ini_options]`/`[tool:pytest]` section), or
a `Cargo.toml`. Run fastci from that project's own root directory, not a
subdirectory of it or a parent directory containing several unrelated
projects. Note that `fastci test` picks a single project type per run (the
first match, in a monorepo with more than one at the same root) — `fastci
guard`, by contrast, detects and runs *every* applicable check in a
monorepo; see [`fastci guard`](#fastci-guard).

**`fastci: no changed files detected, nothing to test`**
Expected on a clean working tree: fastci narrows based on your git diff
(uncommitted changes by default, or `--base <ref>` for a diff against a
specific ref). Make an actual edit, pass `--base` against the right ref, or
use `--all` to run everything regardless of any diff.

**A shallow CI checkout and `--base`**
See [the note under GitHub Actions](#github-actions) — a `fetch-depth: 1`
checkout (or any other shallow/partial one) is detected automatically and
recovered from (an on-demand `git fetch --unshallow` or equivalent), at the
cost of one extra network fetch on the first invocation. A total failure to
fetch (no network, a `--base` ref that doesn't exist at all, permissions)
is reported as a clear error rather than a bare `git` failure.

**`fastci analyze` says there's nothing to analyze, or errors about a
missing API key**
It needs `ANTHROPIC_API_KEY` in the environment, and only has something to
diagnose after a `fastci test` run that actually failed — if the last run
passed, or none has run yet, it says so rather than erroring; see [`fastci
analyze`](#fastci-analyze).

**`fastci guard` reports "could not complete", or skips a check with an
install hint**
That's "couldn't check", not "checked and found a problem" — either the
underlying scanner isn't installed yet (the printed hint is the exact
install command) or it failed to complete for an infrastructure reason
(most commonly, no network access to fetch its vulnerability/advisory
database), not because it actually found and is reporting a vulnerability.
See [`fastci guard`](#fastci-guard) for the full table of scanners and
their install commands.

**Nothing seems to get narrowed — every test still runs**
First check `--why <file-or-package>` (see [Usage](#usage)) for fastci's
actual reasoning about that specific target. Running everything can also
be the deliberately correct outcome: a changed manifest/lockfile, an
unresolvable dynamic import, or a diff wide enough to cross
`--full-run-threshold` are all designed to fall back to a full run rather
than risk silently skipping something that matters — see [Current
limitations](#current-limitations) for what else triggers this per
language.

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
  lifecycle-script scan, a Rust build-script scan, and a
  `--network-report` runtime guardrail on
  `fastci test`/`fastci local` that reports which hosts a run contacted —
  see [`fastci guard`](#fastci-guard) above. `fastci local` is implemented
  for its core scope — auto-detecting CI's diff base branch from
  `.github/workflows/*.yml` and running `fastci test` against it locally —
  see [`fastci local`](#fastci-local) above; faithfully replaying a
  workflow's other steps (e.g. its `uses:` actions) is out of scope for
  this. This closes out every item originally planned for Phase 3.

Language coverage grows incrementally alongside this. Vite `resolve.alias`
resolution, Vitest/Jest monorepo/workspace cross-package resolution, and
function-level impact analysis (Go only for now — see
[Function-level impact analysis (Go)](#function-level-impact-analysis-go))
are all implemented — see [Current limitations](#current-limitations)
above. Extending function-level analysis to Jest/Vitest/pytest is not
planned in the near term: unlike Go, a sound static call graph for
dynamically-typed JS/TS or Python would have a real, material false-
negative risk (missing a test that should run) from ordinary, common
patterns — callbacks, monkey-patching, dynamic dispatch — that Go's static
typing makes tractable to rule out.

## License

MIT — see [LICENSE](LICENSE).
