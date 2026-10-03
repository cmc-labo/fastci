# fastci guard: a user guide and tutorial

This is a hands-on walkthrough of `fastci guard`, fastci's supply-chain
security scanning command: what it checks, how to install what it needs,
and a worked, step-by-step example of every one of its four features -
**PipAudit**, **LifecycleScripts**, **CargoBuildScripts**, and the
**network egress guardrail** (`--network-report`).

This guide is deliberately task-oriented ("do this, see that") rather than
exhaustive. For the full technical reference - exact flags, every output
message, and precisely how a real finding is told apart from an
infrastructure failure - see
[the `fastci guard` section of the main README](../README.md#fastci-guard).
Every command and output shown below was actually run against a real,
throwaway project while writing this guide, not invented.

## Contents

- [What fastci guard does](#what-fastci-guard-does)
- [Before you start](#before-you-start)
- [Part 1: Your first scan](#part-1-your-first-scan)
- [Part 2: PipAudit (Python) - find and fix a vulnerable dependency](#part-2-pipaudit-python---find-and-fix-a-vulnerable-dependency)
- [Part 3: LifecycleScripts (JS/TS) - auditing install-time code execution](#part-3-lifecyclescripts-jsts---auditing-install-time-code-execution)
- [Part 4: CargoBuildScripts (Rust) - auditing build-time code execution](#part-4-cargobuildscripts-rust---auditing-build-time-code-execution)
- [Part 5: The network egress guardrail (`--network-report`)](#part-5-the-network-egress-guardrail---network-report)
- [Putting it together in CI](#putting-it-together-in-ci)
- [Reading the results: finding vs. clean vs. "could not complete"](#reading-the-results-finding-vs-clean-vs-could-not-complete)
- [Troubleshooting](#troubleshooting)
- [Where to go next](#where-to-go-next)

## What fastci guard does

`fastci guard` is one command that detects every applicable ecosystem in
your working directory and runs the right check for each, automatically -
a monorepo with both a `go.mod` and a `package.json` gets every applicable
check for both. Most of its checks run each ecosystem's own official,
trusted scanner; two are fastci's own, for a class of risk no widely-used
official tool covers:

| Feature | Ecosystem | What it is |
| --- | --- | --- |
| `govulncheck` | Go | Official Go vulnerability scanner (reachability-aware) |
| JS audit (`npm`/`pnpm`/`yarn audit`) | JS/TS | Each package manager's own built-in audit |
| **PipAudit** | Python | Wraps the official [`pip-audit`](https://pypi.org/project/pip-audit/) scanner |
| `cargo-audit` | Rust | RustSec's official advisory-database scanner |
| **LifecycleScripts** | JS/TS | fastci's own: flags installed dependencies with an install-time script |
| **CargoBuildScripts** | Rust | fastci's own: flags dependencies with a build-time script |
| **Network egress guardrail** | any | fastci's own: reports hosts a test/build run actually contacted |

This guide covers the four features in **bold** in depth - PipAudit,
LifecycleScripts, CargoBuildScripts, and the network egress guardrail -
since those are the ones with configuration and behavior specific to
fastci itself, rather than being a thin wrapper you'd otherwise invoke
directly.

## Before you start

You need fastci itself installed first - see
[the main README's Quickstart](../README.md#quickstart) if you haven't
done that yet:

```sh
go install github.com/hpscript/fastci/cmd/fastci@latest
fastci --help   # confirms it's installed and on PATH
```

`fastci guard` itself needs no separate installation or config file - it's
already part of the `fastci` binary above. What it *does* need, per
feature you want to use, is the underlying tool for that ecosystem on
`PATH`:

| To use... | Install |
| --- | --- |
| PipAudit | `pip install pip-audit` (or `pipx install pip-audit`) |
| LifecycleScripts | nothing extra - just `npm`/`pnpm`/`yarn install` already run |
| CargoBuildScripts | nothing extra - just `cargo` (comes with [rustup](https://rustup.rs)) |
| Network egress guardrail | nothing extra - built into `fastci test`/`fastci local` |

You don't need all of these installed at once. `fastci guard` checks what
it can and skips the rest with an install hint printed - a Python-only
project doesn't need `cargo` at all, for instance. Each part below is
self-contained; skip to whichever ecosystem(s) you actually use.

## Part 1: Your first scan

Try it against any real project first. `cd` into one (a Go module, a
`package.json`, a Python project, or a Rust crate) and run:

```sh
fastci guard
```

Here's a genuine run against a small demo project (a `package.json`
depending on `esbuild@0.19.8`, with `npm install` already run) - it hits
two of fastci guard's checks at once, since both apply to any `package.json`:

```
$ fastci guard
fastci: running js audit...
# npm audit report

esbuild  <=0.24.2
Severity: moderate
esbuild enables any website to send any requests to the development server and read the response - https://github.com/advisories/GHSA-67mh-4wv8-2f99
fix available via `npm audit fix --force`
Will install esbuild@0.28.2, which is a breaking change
node_modules/esbuild

1 moderate severity vulnerability

To address all issues (including breaking changes), run:
  npm audit fix --force

fastci: js audit reported issues (see above)

fastci: running npm/pnpm/yarn lifecycle scripts...
1 installed dependency defines an install-time lifecycle script - not necessarily malicious, but each one runs code automatically during install, so review any you don't recognize:
  esbuild@0.19.8: postinstall
fastci: npm/pnpm/yarn lifecycle scripts reported issues (see above)

Error: one or more scanners reported vulnerabilities
fastci: one or more scanners reported vulnerabilities
```

Two things to notice, since they come up constantly with `guard`:

1. **It ran two independent checks against the same project** and reported
   on both - `guard` never picks just one check per ecosystem the way
   `fastci test` picks one project type.
2. **A "reported issues" result isn't automatically an emergency.** The
   lifecycle-script finding here (`esbuild`'s `postinstall`) is a real,
   legitimate, well-known package doing exactly what it says - esbuild
   downloads a prebuilt platform binary at install time. `guard` surfaces
   it so a human can confirm that, not because it's inherently bad; see
   [Part 3](#part-3-lifecyclescripts-jsts---auditing-install-time-code-execution)
   for how to actually make that judgment call. The `js audit` finding
   right above it, on the other hand, is an actual, fixable vulnerability -
   that distinction (is this a judgment call, or an actual fix-it finding)
   is specific to each check, covered part-by-part below.

The exit code matters for CI: `guard` exits non-zero exactly when some
check actually reported something (either kind, above) - see
[Reading the results](#reading-the-results-finding-vs-clean-vs-could-not-complete)
for exactly how it decides that, including the important third case
(a check that couldn't even finish, e.g. no network access) which does
**not** count as a finding.

## Part 2: PipAudit (Python) - find and fix a vulnerable dependency

PipAudit wraps [`pip-audit`](https://pypi.org/project/pip-audit/) and
applies whenever a `requirements.txt`, `pyproject.toml`, `Pipfile`,
`setup.py`, or `setup.cfg` is present. With a `requirements.txt`, it's
audited directly - no need to install anything first.

**1. Make a throwaway project with an old, known-vulnerable pin:**

```sh
mkdir pip-demo && cd pip-demo
echo "requests==2.25.1" > requirements.txt
```

**2. Run it:**

```
$ fastci guard
fastci: running pip-audit...
Found 24 known vulnerabilities in 3 packages
Name     Version ID              Fix Versions
-------- ------- --------------- ------------
requests 2.25.1  PYSEC-2023-74   2.31.0
requests 2.25.1  PYSEC-2026-1873 2.32.0
requests 2.25.1  PYSEC-2026-1872 2.32.4
requests 2.25.1  PYSEC-2026-2275 2.33.0
idna     2.10    PYSEC-2024-60   3.7
idna     2.10    PYSEC-2026-215  3.15
urllib3  1.26.20 PYSEC-2026-1999 2.5.0
urllib3  1.26.20 PYSEC-2026-1998 2.6.0
...

fastci: pip-audit reported issues (see above)

Error: one or more scanners reported vulnerabilities
fastci: one or more scanners reported vulnerabilities
```

Every row names the vulnerable package, its installed version, the
advisory ID, and the version that fixes it - `requests` itself is fixed as
of `2.31.0`, but `pip-audit` also caught two of *its own* transitive
dependencies (`idna`, `urllib3`) pinned to old, separately-vulnerable
versions. Note the `Fix Versions` column directly tells you what to do
next - no separate lookup needed.

**3. Fix it - bump the pin to a version past every listed advisory:**

```sh
echo "requests==2.33.0" > requirements.txt
```

**4. Confirm it's clean:**

```
$ fastci guard
fastci: running pip-audit...
No known vulnerabilities found

fastci: pip-audit: no issues found

fastci: no vulnerabilities found
```

That's the whole loop: run `fastci guard`, read the `Fix Versions` column,
bump the pin, re-run to confirm.

**No `requirements.txt`?** If your project only has a `pyproject.toml` (or
similar) with no committed `requirements.txt`, PipAudit falls back to
auditing whatever's installed in the *active* Python environment instead -
**activate your project's virtualenv first** (so its own `python3` is
first on `PATH`) before running `fastci guard`, or you risk silently
auditing an unrelated environment instead of your project's. See
[the PipAudit section of the README](../README.md#pipaudit-python) for
exactly why this matters and how fastci steers it correctly when it can.

## Part 3: LifecycleScripts (JS/TS) - auditing install-time code execution

LifecycleScripts applies to any `package.json`, and needs `node_modules`
to already exist (run `npm install`/`pnpm install`/`yarn install` first).
It's a pure, read-only scan - it never runs an install or executes
anything itself - looking for any installed dependency whose own
`package.json` defines a `preinstall`, `install`, or `postinstall` script.

**1. Install a real, legitimate package that uses one** - `esbuild` is a
good example: it fetches a prebuilt platform-specific binary via a
`postinstall` script.

```sh
mkdir lifecycle-demo && cd lifecycle-demo
echo '{"name":"demo","version":"1.0.0","dependencies":{"esbuild":"0.28.2"}}' > package.json
npm install
```

**2. Run it:**

```
$ fastci guard
fastci: running js audit...
found 0 vulnerabilities

fastci: js audit: no issues found

fastci: running npm/pnpm/yarn lifecycle scripts...
1 installed dependency defines an install-time lifecycle script - not necessarily malicious, but each one runs code automatically during install, so review any you don't recognize:
  esbuild@0.28.2: postinstall
fastci: npm/pnpm/yarn lifecycle scripts reported issues (see above)

Error: one or more scanners reported vulnerabilities
fastci: one or more scanners reported vulnerabilities
```

**3. Decide what to do about it.** Unlike PipAudit above, there's no
"Fix Versions" column here, because this isn't a vulnerability report -
it's a list of dependencies that *can* run code automatically, which you
have to judge yourself:

- **Recognize the package and its reason for using a lifecycle script?**
  (esbuild downloading its own binary, `puppeteer` downloading a browser,
  `husky`/`core-js` setting up git hooks or polyfill data - all common,
  legitimate, well-known cases.) Nothing to do - this is expected, and
  re-running `guard` will keep reporting it every time, which is correct:
  it's not meant to be silenced, just visible.
- **Don't recognize it, or it's a transitive dependency you didn't
  deliberately choose?** Look at what the script actually runs (every
  hit's `package.json` lists the exact script command) and decide whether
  it belongs in your dependency tree at all.

There's deliberately no ignore-list or config to suppress a specific
package here (see [the README](../README.md#lifecyclescripts-jsts)) - the
check's entire job is just to make sure a human looks, not to pre-judge
which ones are fine.

## Part 4: CargoBuildScripts (Rust) - auditing build-time code execution

CargoBuildScripts is the Rust-ecosystem equivalent of LifecycleScripts,
applying to any `Cargo.toml`, needing only `cargo` itself (no separate
install). Instead of an install-time script, it flags a **dependency**
(direct or transitive - never your own crate) that defines a custom build
script (conventionally `build.rs`), which Cargo runs automatically at
*build* time, before your crate's own code even compiles.

**1. Make a throwaway project with a path dependency that has one:**

```sh
mkdir -p cargo-demo/dep/src cargo-demo/src
cd cargo-demo
cat > Cargo.toml <<'EOF'
[package]
name = "app"
version = "0.1.0"
edition = "2021"

[dependencies]
dep = { path = "dep" }
EOF
echo 'fn main() {}' > src/main.rs
cat > dep/Cargo.toml <<'EOF'
[package]
name = "dep"
version = "0.1.0"
edition = "2021"
build = "build.rs"
EOF
echo 'fn main() {}' > dep/build.rs
echo 'pub fn hello() {}' > dep/src/lib.rs
```

**2. Run it:**

```
$ fastci guard
fastci: running cargo-audit...
    Fetching advisory database from `https://github.com/RustSec/advisory-db.git`
      Loaded 1288 security advisories (from ~/.cargo/advisory-db)
    Scanning Cargo.lock for vulnerabilities (2 crate dependencies)

fastci: cargo-audit: no issues found

fastci: running cargo build scripts...
1 dependency defines a custom build script - not necessarily malicious, but each one runs arbitrary code automatically at build time, so review any you don't recognize:
  dep@0.1.0: /path/to/cargo-demo/dep/build.rs
fastci: cargo build scripts reported issues (see above)

Error: one or more scanners reported vulnerabilities
fastci: one or more scanners reported vulnerabilities
```

Notice `cargo-audit` ran clean (no known vulnerability in this toy
dependency), while `cargo build scripts` still flagged `dep`'s build
script - these two checks are entirely independent, and a dependency can
trip one, the other, both, or neither.

**3. Decide what to do about it** - exactly the same judgment call as
LifecycleScripts above: linking a system C library, generating code from a
schema, or emitting `cfg` flags for conditional compilation are all
common, legitimate reasons for a crate to have a `build.rs`. The reported
path (`dep@0.1.0: /path/to/.../dep/build.rs`) is the actual file - open it
if you want to see exactly what it does. Your own crate (and every member
of your own `[workspace]`, if any) is never flagged, no matter what its
own `build` field says - only third-party dependencies are in scope.

## Part 5: The network egress guardrail (`--network-report`)

Unlike the three checks above, this isn't a `Checker` you find applicable
by project type - it's an opt-in flag, `--network-report`, on `fastci
test` (and `fastci local`), since it has to wrap the actual test/build
process running rather than scan anything statically.

**1. Take any project whose tests make a real network call** - here's a
minimal one, a Go test that does an HTTP GET:

```go
// netcheck_test.go
package netcheck

import (
	"net/http"
	"testing"
)

func TestFetch(t *testing.T) {
	resp, err := http.Get("http://example.com")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
}
```

**2. Run with the flag:**

```sh
fastci test --network-report
```

```
$ fastci test --network-report
fastci: selected 1/1 test target(s) (go, 0% skipped)
  * netcheck
ok  	netcheck	0.240s
fastci: network report: 1 host(s) contacted:
  example.com:80
```

If nothing in the run makes an outbound connection, it says so instead:

```
fastci: network report: no outbound connections observed
```

**What this is for**: noticing a dependency (or your own code) phoning
home somewhere you didn't expect during a build or test run - especially
relevant right after investigating a LifecycleScripts or
CargoBuildScripts finding from Parts 3-4 above, if you want to confirm
exactly what a suspicious install/build script actually reached over the
network.

**What it can and can't see**: it works by pointing the run at a small
local proxy via `HTTP_PROXY`/`HTTPS_PROXY` (restored to whatever they were
before, once the run finishes) and reports every host contacted through
it. It never decrypts HTTPS - it only reads the plaintext `CONNECT
host:port` line to learn the destination. It's purely informational and
never blocks the run based on what it sees. The one real limitation: it
only sees traffic from a tool that actually honors those proxy
environment variables - a tool making a raw socket connection or
resolving/dialing an IP directly would bypass it invisibly. See
[the full writeup in the README](../README.md#network-egress-guardrail---network-report)
for more detail.

## Putting it together in CI

A minimal GitHub Actions job running `fastci guard` on every pull request,
installing only the scanners your project actually needs (delete the
blocks for ecosystems you don't use, matching the commented-out style in
[the main README's CI example](../README.md#github-actions)):

```yaml
name: guard
on:
  pull_request:

jobs:
  guard:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go install github.com/hpscript/fastci/cmd/fastci@latest
      - run: go install golang.org/x/vuln/cmd/govulncheck@latest

      # JS/TS projects: LifecycleScripts needs node_modules to exist.
      # - uses: actions/setup-node@v4
      #   with:
      #     node-version: 22
      # - run: npm ci

      # Python projects: PipAudit
      # - uses: actions/setup-python@v5
      #   with:
      #     python-version: "3.12"
      # - run: pip install pip-audit

      # Rust projects: CargoBuildScripts only needs `cargo` itself;
      # cargo-audit needs installing separately.
      # - run: cargo install cargo-audit

      - run: fastci guard
```

`fastci guard` exits non-zero when any check reports a real finding, so
this job fails the same way a normal test job would - no extra
`continue-on-error` or result-parsing needed. A scanner whose tool isn't
installed (if you deleted a block above but the project still matches
that ecosystem) is skipped with an install hint printed, and does **not**
fail the job by itself - see the next section for exactly why.

## Reading the results: finding vs. clean vs. "could not complete"

Every check in this guide reports exactly one of three outcomes, and only
one of them means "go fix something":

| Outcome | Example message | Fails the job? |
| --- | --- | --- |
| Real finding | `fastci: pip-audit reported issues (see above)` | **Yes** |
| Clean | `fastci: pip-audit: no issues found` | No |
| Couldn't complete | `fastci: govulncheck: could not complete - ...` | No - same bucket as "tool not installed" |

The third case matters in CI specifically: a scanner that couldn't fetch
its vulnerability database over the network (a flaky connection, a
firewalled runner) is reported, but doesn't fail the build by itself and
doesn't stop any other applicable check in the same run - `fastci guard`
still prints a note that something was skipped, rather than silently
claiming a clean bill of health. The full mechanics of exactly how a real
finding is told apart from this case per tool - which matters most for
PipAudit, since `pip-audit` reuses the same exit code for both - are
covered in
[Distinguishing a real finding from an infrastructure failure](../README.md#distinguishing-a-real-finding-from-an-infrastructure-failure)
in the main README.

## Troubleshooting

**PipAudit: "This may result in unintuitive audits" warning, or results
that don't look right**
You're hitting the active-environment fallback (no `requirements.txt`) -
see the note at the end of [Part 2](#part-2-pipaudit-python---find-and-fix-a-vulnerable-dependency).

**LifecycleScripts: skipped with "no node_modules directory found"**
Run `npm install` (or `pnpm`/`yarn install`) first - this check reads
what's already installed on disk, it never installs anything itself.

**CargoBuildScripts: skipped with "cargo not found on PATH"**
Install Rust via [rustup](https://rustup.rs) - no separate tool needed
beyond `cargo` itself.

**`--network-report` reports a host you don't recognize**
That's the point - cross-reference it against a LifecycleScripts or
CargoBuildScripts finding from the same project (Parts 3-4) if you
suspect an install/build script is the source, or against your test
suite's own known external dependencies if not.

**A check reports "could not complete" in CI but works locally**
Almost always a network-egress restriction on the CI runner itself
(firewalled, no outbound access to the advisory database's host) - see
[Reading the results](#reading-the-results-finding-vs-clean-vs-could-not-complete)
above for why this is intentionally not treated as a failure.

For anything not specific to `guard` (install/PATH issues with fastci
itself, etc.), see
[the main README's Troubleshooting section](../README.md#troubleshooting).

## Where to go next

- [`fastci guard` in the main README](../README.md#fastci-guard) - the
  full reference: every flag, every checker's exact detection rule, and
  the complete exit-code/reporting semantics.
- [PipAudit (Python)](../README.md#pipaudit-python),
  [LifecycleScripts (JS/TS)](../README.md#lifecyclescripts-jsts),
  [CargoBuildScripts (Rust)](../README.md#cargobuildscripts-rust),
  [the network egress guardrail](../README.md#network-egress-guardrail---network-report) -
  each feature's own deep-dive reference section.
- [`fastci test`](../README.md#usage) and
  [`fastci local`](../README.md#fastci-local) - fastci's other half, the
  impact-driven test runner this guide's `--network-report` flag attaches
  to.
