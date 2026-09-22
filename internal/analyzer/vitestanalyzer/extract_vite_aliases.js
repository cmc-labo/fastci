// Loads an esbuild-bundled Vite/Vitest config module (already transpiled
// to CommonJS by the Go side - see viteconfig.go) and prints its resolved
// `resolve.alias` entries to stdout as a JSON object: {"find": "absolute
// replacement path", ...}.
//
// Handles every shape Vite itself accepts for the config export: a plain
// object, `defineConfig(...)` (which is just the identity function, or
// close enough for our purposes), a function - sync or async - receiving
// `({command, mode, ssrBuild})`, and both the object form
// (`{ '@': '/src' }`) and array form
// (`[{ find: '@', replacement: '/src' }]`) of `resolve.alias`. A `find`
// that isn't a plain string (Vite also allows a RegExp there) is skipped -
// esbuild's own `Alias` option only supports exact/prefix string matching,
// so a RegExp-keyed rule has no equivalent here; imports that would only
// have matched it are simply left exactly as unresolved as they already
// are without this script existing at all, not treated as an error.
'use strict';
const path = require('path');

async function main() {
  const bundlePath = process.argv[2];
  const configPath = process.argv[3];

  const mod = require(bundlePath);
  let config = mod && mod.__esModule ? mod.default : mod;
  if (typeof config === 'function') {
    config = config({ command: 'serve', mode: 'test', ssrBuild: false });
  }
  config = await config;

  const alias = config && config.resolve && config.resolve.alias;
  const configDir = path.dirname(configPath);
  const out = {};

  const addEntry = (find, replacement) => {
    if (typeof find !== 'string' || typeof replacement !== 'string') {
      return;
    }
    if (!replacement.startsWith('.') && !replacement.startsWith('/')) {
      return; // a bare package specifier, not a filesystem path - nothing for us to point esbuild's Alias at.
    }
    if (Object.prototype.hasOwnProperty.call(out, find)) {
      return; // first match wins, matching Vite's own array-order semantics.
    }
    out[find] = path.resolve(configDir, replacement);
  };

  if (Array.isArray(alias)) {
    for (const entry of alias) {
      if (entry && typeof entry === 'object') {
        addEntry(entry.find, entry.replacement);
      }
    }
  } else if (alias && typeof alias === 'object') {
    for (const find of Object.keys(alias)) {
      addEntry(find, alias[find]);
    }
  }

  process.stdout.write(JSON.stringify(out));
}

main().catch((err) => {
  process.stderr.write(String((err && err.stack) || err) + '\n');
  process.exit(1);
});
