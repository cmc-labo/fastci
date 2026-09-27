# fastci vs. Nx and Turborepo

Nx and Turborepo are monorepo build/task orchestrators: they run tasks
(build, lint, test, deploy, ...) across a workspace of projects, and skip
or cache a task if its declared inputs haven't changed. fastci does one
narrower job — decide which tests a given diff actually needs to run —
and does it without requiring a monorepo, a workspace config, or a
JS/TS-based toolchain. This page is a fact-based comparison for people
choosing between them, not a claim that fastci replaces either: if you
need build pipelines, task graphs, generators/scaffolding, or a mature
remote-cache SaaS, Nx and Turborepo do that and fastci doesn't attempt to.

Every specific claim below is either directly checkable from the linked
official docs/issue, or was measured with the scripts in
[docs/benchmarks](benchmarks/README.md) — no unverified performance claims.

## At a glance

| | fastci | Nx | Turborepo |
| --- | --- | --- | --- |
| Native language support | Go, Rust, Python, JS/TS, in one tool | JS/TS native; Go/Rust/Python/Java/.NET via plugins reading each toolchain's own manifest | JS/TS only ([npm/yarn/pnpm ecosystem](https://turborepo.dev/docs)) |
| Impact-analysis granularity | File-level for JS/TS and Python; package-level for Go and crate-level for Rust, each refined further by real import resolution (not just "this project imports that project"); Go gets a further, function-level pass on top (see below) | Project-level ([`nx affected` operates on the project graph](https://nx.dev/docs/features/multi-language-support)) | Package-level ([confirmed in Turborepo's own docs](https://turborepo.dev/docs/crafting-your-repository/caching): the affected feature "operates at the package level, not file-level") |
| Requires a monorepo/workspace | No — works on a single existing repo, one package or many | Yes — `nx.json` plus either `project.json` per project or a language plugin | No for the tool itself (works in [single-package workspaces](https://turborepo.dev/docs) too), but only orchestrates JS/TS tasks either way |
| New config file to adopt | None — reads your existing `go.mod`/`package.json`/`pyproject.toml`/`Cargo.toml` as-is | `nx.json` (+ `project.json` per project, unless a plugin infers it) | `turbo.json` |
| AI-assisted failure diagnosis | Yes (`fastci analyze`) | No | No |
| Supply-chain / runtime security scanning | Yes (`fastci guard`: govulncheck, npm/pip/cargo audit, lifecycle-script scan, network-egress report) | No | No |
| Build task graphs, generators, dev-server orchestration | No | Yes | Yes |
| Remote cache maturity | Minimal (a plain HTTP GET/PUT protocol you point at anything — see [Test-result cache](../README.md#test-result-cache-local-and-distributed)) | Mature (Nx Cloud) | Mature (Vercel Remote Cache) |

## Why the granularity difference is real, not marketing

This is the most substantive, checkable difference, so it's worth walking
through concretely rather than just asserting it.

**Turborepo's own documentation is explicit about the floor**: its
`affected` selection "operates at the package level, not file-level,
meaning you can't selectively run only tests for changed files within a
package using the built-in affected selection." Turborepo's caching is a
content hash over a task's *declared* input globs — if the hash of a
package's inputs matches a previous run, the whole task's result is
replayed, regardless of which specific file inside the package changed or
which specific tests would exercise it.

**Nx's own docs describe `nx affected` as operating on the project
graph** — a graph of *projects*, built from each toolchain's declared
package-level dependencies (`Cargo.toml`, `pyproject.toml`, `build.gradle`,
etc. for non-JS languages). A real user filed
[nx/issues/21964](https://github.com/nrwl/nx/issues/21964) illustrating
exactly this limitation: after changing one file in a shared library, Nx
rebuilt every downstream project, including one that only depended on a
*different* file in that same library and had nothing to do with the
actual change. The issue is closed/labeled outdated, but the underlying
architecture it describes — affected detection at the project graph
level — is the documented design, not a bug that was fixed.

**fastci's floor is a file** (for Go, Rust, Python, JS/TS all the same
way — actual import resolution via each language's own tooling: `go
list`/`go/packages` for Go, `cargo metadata` for Rust, Python's own `ast`
module, `esbuild`'s resolver for JS/TS) **and, for Go specifically, a
function** — see [Function-level impact analysis
(Go)](../README.md#function-level-impact-analysis-go). Changing one
function's body in a large Go package can select only the specific test
functions that actually, transitively call it, not every test in the
package, let alone every project that imports the package.

## Where Nx and Turborepo are ahead

To be fair, in exchange for that narrower scope:

- **Remote caching.** Nx Cloud and Vercel's Remote Cache are mature,
  hosted, production-grade caching services. fastci's [remote
  cache](../README.md#test-result-cache-local-and-distributed) is a
  minimal, self-hosted-by-you HTTP GET/PUT protocol - functional, but not
  a managed service with a dashboard.
- **Task orchestration.** Both are full build-system task graphs: build →
  lint → test → deploy pipelines, with dependency-aware task ordering.
  fastci only decides which tests to run; it doesn't orchestrate any other
  kind of task.
- **Ecosystem and maturity.** Both have years of production use, large
  communities, generators/scaffolding, editor integrations, and dedicated
  companies behind them. fastci is a much younger, narrower-scope project.
- **Nx's non-JS language coverage is broader in one respect**: it also has
  plugins for Java and .NET, which fastci doesn't support at all yet.

## When each makes sense

- **Nx or Turborepo**: you're building (or already have) a JS/TS-centric
  monorepo and want task orchestration, generators, and a managed remote
  cache across your whole workspace.
- **fastci**: you want impact-driven test selection specifically - in a
  single-package repo, a polyglot codebase (Go + Python + Rust + JS/TS),
  or a monorepo where a JS-first tool's project-level affected detection
  is coarser than you'd like for your Go/Rust/Python packages - without
  adopting a new workspace structure or config format to get it.
- **Both together**: nothing stops using fastci for a polyglot monorepo's
  non-JS packages (or for extra file/function-level precision on top of
  Nx's project-level affected detection) while Nx or Turborepo handles the
  rest of the task graph.
