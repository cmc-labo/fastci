# fastci now understands your JS/TS monorepo's real dependency graph

Vitest and Jest test selection now resolves cross-package imports inside an
npm/yarn/pnpm **workspace monorepo** - `import {x} from '@myorg/utils'`
from a sibling package - down to the exact file, with zero configuration.
This page is a fact-based look at what that means and why it matters, in
the same spirit as [fastci vs. Nx/Turborepo](comparison.md): every claim
below is either a real, reproducible command you can run yourself, or a
link to the source behind it - no unverified claims.

## The problem this solves

JS/TS monorepos split code across packages on purpose - a shared `utils`
or `core` package, consumed by several app packages - but that split has
historically been a blind spot for impact-driven test selection, not a
convenience boundary. Nx's own affected-detection architecture is a
documented example of exactly this gap: a real user
[filed an issue](https://github.com/nrwl/nx/issues/21964) showing that
changing one file in a shared library made Nx rebuild *every* downstream
project - including one that only depended on a *different*, unrelated
file in that same library. That happens because affected detection there
operates on a graph of whole *projects*, not files - see
[fastci vs. Nx/Turborepo](comparison.md#why-the-granularity-difference-is-real-not-marketing)
for the full breakdown.

A narrower, more dangerous version of the same gap existed inside fastci's
own JS/TS analysis until this feature landed: a bare specifier import like
`@myorg/utils` was, by necessity, treated exactly like a real external
`node_modules` dependency - because from a single package's point of view,
without reading the workspace's own manifest, there's no way to tell the
two apart. That's not just an *over*-approximation (running more tests
than necessary, like Nx's project-level floor) - it's a potential
*under*-approximation: a cross-package import edge the graph can't see is
an edge impact analysis can't walk, which means a test that should be
selected because of a changed shared file could be silently skipped
instead. Getting monorepo workspace resolution right isn't just about a
better skip rate; it's about whether the narrowed test set can be trusted
at all in exactly the kind of project (a JS/TS monorepo) most likely to
reach for impact-driven test selection in the first place.

## What fastci does now

Both Vitest and Jest resolve a cross-package workspace import to the real
member package's actual files - both its bare main entry
(`@myorg/utils`) and any subpath (`@myorg/utils/helpers`) - the same way
your bundler would, using [esbuild](https://esbuild.github.io/)'s own
resolver pointed at the right directory. This is:

- **Fully static.** It reads the workspace root's `package.json`
  `"workspaces"` field (npm/yarn) or `pnpm-workspace.yaml`'s `packages:`
  list (pnpm) to map each member package's name to its directory - the
  same manifest you already committed, not a new config file to adopt.
- **Zero-install.** Like the rest of fastci's JS/TS analysis, this never
  requires `npm install`/`pnpm install`/`yarn install` to have been run
  first - it doesn't need `node_modules` to exist to build the graph.
- **File-level, even across the package boundary.** The resolved edge
  lands on the exact file inside the dependency package, not "this
  package depends on that package" - the same precision fastci already
  applies within a single package, now carried through a workspace
  boundary instead of stopping at it.

## See it work

Here's a real, minimal npm workspace - three packages, `@demo/utils`
depended on by `@demo/api`, with `@demo/web` unrelated to either - and the
exact commands to reproduce this yourself.

```sh
mkdir -p monorepo-demo/packages/{utils,api,web}/src
mkdir -p monorepo-demo/packages/{utils,api,web}/__tests__
cd monorepo-demo

cat > package.json <<'EOF'
{
  "name": "monorepo-demo",
  "private": true,
  "workspaces": ["packages/*"],
  "devDependencies": { "jest": "29.7.0" }
}
EOF

cat > packages/utils/package.json <<'EOF'
{"name": "@demo/utils", "version": "1.0.0", "main": "src/index.js"}
EOF
cat > packages/utils/src/index.js <<'EOF'
function add(a, b) { return a + b; }
module.exports = { add };
EOF
cat > packages/utils/__tests__/index.test.js <<'EOF'
const { add } = require('../src/index');
test('add', () => { expect(add(2, 3)).toBe(5); });
EOF

cat > packages/api/package.json <<'EOF'
{"name": "@demo/api", "version": "1.0.0", "dependencies": {"@demo/utils": "1.0.0"}}
EOF
cat > packages/api/src/index.js <<'EOF'
const { add } = require('@demo/utils');
module.exports = { sumHandler: (req) => add(req.a, req.b) };
EOF
cat > packages/api/__tests__/index.test.js <<'EOF'
const { sumHandler } = require('../src/index');
test('sumHandler', () => { expect(sumHandler({ a: 1, b: 2 })).toBe(3); });
EOF

cat > packages/web/package.json <<'EOF'
{"name": "@demo/web", "version": "1.0.0"}
EOF
cat > packages/web/src/index.js <<'EOF'
module.exports = { render: () => '<div>hello</div>' };
EOF
cat > packages/web/__tests__/index.test.js <<'EOF'
const { render } = require('../src/index');
test('render', () => { expect(render()).toContain('hello'); });
EOF

npm install
git init -q && git add -A && git commit -q -m init
```

Now edit `@demo/utils`'s source - the file nothing in `@demo/web` has ever
touched - and ask fastci what's affected:

```
$ echo 'function multiply(a,b){return a*b;} module.exports={add,multiply};' >> packages/utils/src/index.js
$ fastci test --dry-run -v
fastci: 1 changed file(s):
  packages/utils/src/index.js
fastci: selected 2/3 test target(s) (jest, 33% skipped)
    packages/api/__tests__/index.test.js
    packages/utils/__tests__/index.test.js
fastci: dry-run, not executing tests
```

`@demo/api`'s test is selected - correctly - purely because of a bare
`require('@demo/utils')` in `packages/api/src/index.js`, resolved across
the workspace boundary to the exact file that changed. `--why` shows the
real dependency chain fastci actually walked:

```
$ fastci test --why packages/api/__tests__/index.test.js
fastci: why is "packages/api/__tests__/index.test.js" selected?
  Selected because of this dependency chain:
    packages/utils/src/index.js (changed)
    -> packages/api/src/index.js
    -> packages/api/__tests__/index.test.js
```

And `@demo/web`, which never imports `@demo/utils` at all, is correctly
left out - not by omission, but because fastci can show its work:

```
$ fastci test --why packages/web/__tests__/index.test.js
fastci: why is "packages/web/__tests__/index.test.js" selected?
  NOT selected: no changed file's effect reaches this target through the dependency graph.
```

Reverting that edit and instead changing only `packages/web/src/index.js`
demonstrates the converse - precision in both directions, not just a
bias toward including more:

```
$ fastci test --dry-run -v
fastci: 1 changed file(s):
  packages/web/src/index.js
fastci: selected 1/3 test target(s) (jest, 67% skipped)
    packages/web/__tests__/index.test.js
fastci: dry-run, not executing tests
```

Running the narrowed set for real (`fastci test -v`, no `--dry-run`)
against the first scenario actually executes both selected suites and
passes, exactly as Jest itself would report running them directly - this
isn't a selection-only preview disconnected from real execution.

This three-package repo is obviously small - real monorepos have far more
packages and a much lower fraction genuinely touched by any given shared-
file change, which is where the skip-rate advantage compounds. The
*shape* of the result (api selected, web excluded, each with a legible
reason) is what to look for when you try this against your own workspace,
not the specific 33%/67% figures above, which are an artifact of this
three-package toy example, not a benchmark claim.

## What it needs, honestly

- Run fastci from the **workspace root** - the same place the
  `"workspaces"`/`pnpm-workspace.yaml` manifest lives. From inside a
  single member package, that package is analyzed in isolation and
  cross-package imports stay external, as before this feature existed.
- An exclusion glob (`!pattern` in `pnpm-workspace.yaml`) isn't honored
  yet - a member matched by a broader inclusion pattern despite an unread
  exclusion is only ever a false *inclusion* (safe direction: more tests
  run, never fewer), never a missed one.
- This applies to both Vitest and Jest identically - see
  [Current limitations](../README.md#current-limitations) in the main
  README for the complete, per-language technical reference, including
  how this interacts with `tsconfig.json` path aliases and Vite's
  `resolve.alias`.

## Try it

No upgrade, flag, or config needed - if you're already on a recent fastci,
this is already active for any npm/yarn/pnpm workspace:

```sh
go install github.com/hpscript/fastci/cmd/fastci@latest
cd your-monorepo   # the workspace root
fastci test --dry-run -v
```

See the [main README's Quickstart](../README.md#quickstart) if you haven't
installed fastci yet, and
[fastci vs. Nx/Turborepo](comparison.md) for how this fits into the
broader granularity comparison against the two established monorepo
orchestrators.
