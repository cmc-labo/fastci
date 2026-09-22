package vitestanalyzer

import (
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/evanw/esbuild/pkg/api"
)

//go:embed extract_vite_aliases.js
var extractAliasesScript []byte

var viteConfigFileNames = []string{
	"vite.config.ts", "vite.config.js", "vite.config.mjs", "vite.config.cjs",
	"vite.config.mts", "vite.config.cts",
}

// findViteAliasConfigFile returns the config file Vitest would actually
// load its resolve.alias settings from: vitest.config.* takes priority
// over vite.config.* when both exist, matching Vitest's own documented
// precedence (Vitest merges vite.config.* in only when no vitest.config.*
// is present).
func findViteAliasConfigFile(dir string) (string, bool) {
	for _, name := range vitestConfigFileNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	for _, name := range viteConfigFileNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// resolveViteAliases extracts a project's Vite `resolve.alias` config (see
// the package doc) by bundling the config file with esbuild and actually
// executing it under Node - the same way Vite/Vitest themselves load their
// config, so `path.resolve(__dirname, ...)` calls, conditional/function
// config exports, and both shapes of `resolve.alias` are all handled
// correctly rather than approximated.
//
// This is entirely best-effort: no config file, no Node on PATH, the
// config's own imports (e.g. "vite" itself, for defineConfig) not being
// installed, or any other failure along the way all just return a nil
// map rather than an error, leaving alias-only-reachable imports exactly
// as invisible-to-the-graph as they were before this existed (see the
// package doc) instead of failing the whole analysis over an optional
// enhancement.
func resolveViteAliases(dir string) map[string]string {
	configPath, ok := findViteAliasConfigFile(dir)
	if !ok {
		return nil
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return nil
	}

	// __dirname/__filename are left as ordinary runtime CJS globals by
	// esbuild's bundler - they aren't rewritten to the original source
	// file's location, so left alone they'd evaluate, at Node runtime, to
	// wherever the *bundle* physically ends up on disk (the scratch temp
	// directory below), not to configPath's real directory. Since
	// `path.resolve(__dirname, "src")` is the standard idiom nearly every
	// real Vite config uses for its alias targets, getting this wrong
	// would silently point every alias at the wrong absolute path rather
	// than simply not resolving it - Define forces both back to configPath's
	// actual location at bundle time, before that ambiguity can arise.
	dirnameLit, err := json.Marshal(filepath.Dir(configPath))
	if err != nil {
		return nil
	}
	filenameLit, err := json.Marshal(configPath)
	if err != nil {
		return nil
	}

	result := api.Build(api.BuildOptions{
		EntryPoints:   []string{configPath},
		Bundle:        true,
		Write:         false,
		Platform:      api.PlatformNode,
		Format:        api.FormatCommonJS,
		Packages:      api.PackagesExternal,
		LogLevel:      api.LogLevelSilent,
		AbsWorkingDir: dir,
		Define: map[string]string{
			"__dirname":  string(dirnameLit),
			"__filename": string(filenameLit),
		},
	})
	if len(result.Errors) > 0 || len(result.OutputFiles) == 0 {
		return nil
	}

	// Written inside dir/.fastci-cache (the same scratch/cache location
	// internal/testcache and the failure log already use) rather than the
	// system temp dir: Node's require() resolves a bare specifier like
	// "vite" (which a config's `import { defineConfig } from 'vite'`
	// compiles down to) by walking up from the requiring file's own
	// directory looking for node_modules, so the bundle has to live
	// somewhere under dir for that walk to ever reach dir/node_modules.
	cacheDir := filepath.Join(dir, ".fastci-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil
	}
	if gitignore := filepath.Join(cacheDir, ".gitignore"); !fileExists(gitignore) {
		_ = os.WriteFile(gitignore, []byte("*\n"), 0o644)
	}
	scratchDir, err := os.MkdirTemp(cacheDir, "vite-alias-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(scratchDir)

	bundlePath := filepath.Join(scratchDir, "config.bundle.cjs")
	if os.WriteFile(bundlePath, result.OutputFiles[0].Contents, 0o644) != nil {
		return nil
	}
	scriptPath := filepath.Join(scratchDir, "extract.js")
	if os.WriteFile(scriptPath, extractAliasesScript, 0o644) != nil {
		return nil
	}

	out, err := exec.Command(node, scriptPath, bundlePath, configPath).Output()
	if err != nil {
		return nil
	}

	var aliases map[string]string
	if json.Unmarshal(out, &aliases) != nil {
		return nil
	}
	return aliases
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
