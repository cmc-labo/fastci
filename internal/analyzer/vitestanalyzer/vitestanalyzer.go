// Package vitestanalyzer implements the fastci analyzer.Analyzer interface
// for TypeScript/JavaScript projects tested with Vitest.
//
// Like jestanalyzer, it tests at file granularity and resolves imports by
// running the project's source through esbuild's real resolver - relative
// imports, tsconfig `paths`/`baseUrl` aliases, extension/index resolution,
// and import()/require() calls with a static string or resolvable
// template-literal argument are all handled the same way jestanalyzer does
// (see internal/analyzer/dynimport for the dynamic-import safety net shared
// by both). Unlike jestanalyzer, it does not resolve Vite's own
// `resolve.alias` config (in vite.config.*/vitest.config.*): unlike Jest's
// moduleNameMapper, which is JSON-shaped and can be read as data, a Vite
// alias list lives inside arbitrary JS/TS config code, so there's no static
// config format to parse the way loadModuleNameMapper does for Jest. An
// import resolved only through such an alias is invisible to the graph -
// see the README for this limitation.
package vitestanalyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/hpscript/fastci/internal/analyzer/dynimport"
	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/runner"
)

// Analyzer is the Vitest implementation of analyzer.Analyzer.
type Analyzer struct{}

// New returns a Vitest analyzer.
func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) Name() string { return "vitest" }

var vitestConfigFileNames = []string{
	"vitest.config.ts", "vitest.config.js", "vitest.config.mjs", "vitest.config.cjs",
	"vitest.config.mts", "vitest.config.cts",
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// Detect reports whether dir has a Vitest config file (vitest.config.*) or
// vitest listed as a dependency in package.json. Unlike Jest, Vitest has no
// equivalent of a "jest" field embedded in package.json, and a bare
// vite.config.* isn't treated as a signal on its own - plenty of Vite
// projects have no tests at all, and whether one configures a `test` block
// isn't something worth statically parsing just to detect - see the
// package doc for why that config can't be read as data anyway.
func (*Analyzer) Detect(dir string) (bool, error) {
	for _, name := range vitestConfigFileNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true, nil
		}
	}

	pkg, err := readPackageJSON(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if _, ok := pkg.Dependencies["vitest"]; ok {
		return true, nil
	}
	if _, ok := pkg.DevDependencies["vitest"]; ok {
		return true, nil
	}
	return false, nil
}

func readPackageJSON(dir string) (*packageJSON, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, fmt.Errorf("vitestanalyzer: parsing package.json: %w", err)
	}
	return &pkg, nil
}

var trackedExt = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".mjs": true, ".cjs": true, ".mts": true, ".cts": true,
}

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	".next": true, "out": true, "coverage": true, ".turbo": true, ".cache": true,
}

func discoverSourceFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".d.ts") {
			return nil // ambient type declarations have no runtime behavior
		}
		if trackedExt[filepath.Ext(path)] {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// metafile mirrors the subset of esbuild's --metafile JSON we need: for
// each input file, the files it imports and whether each import resolved
// to something outside the project (node_modules, i.e. not part of the
// graph we track).
type metafile struct {
	Inputs map[string]struct {
		Imports []struct {
			Path     string `json:"path"`
			External bool   `json:"external"`
		} `json:"imports"`
	} `json:"inputs"`
}

// Build discovers every tracked source file under dir and asks esbuild to
// resolve each one's imports - relative paths, tsconfig `paths`/`baseUrl`
// aliases, and extension/index resolution are all handled by esbuild's real
// resolver. Bare specifiers that resolve into node_modules are treated as
// external and are not tracked as graph edges.
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
	files, err := discoverSourceFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("vitestanalyzer: discovering source files: %w", err)
	}
	g := graph.New()
	// Every node here is a single file (unlike Go/Cargo, where many files
	// legitimately share one package/crate node) - see the field doc.
	g.DisableDirFallback = true
	if len(files) == 0 {
		return g, nil
	}

	entryPoints := make([]string, len(files))
	for i, f := range files {
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			return nil, fmt.Errorf("vitestanalyzer: %w", err)
		}
		entryPoints[i] = filepath.ToSlash(rel)
	}

	opaqueFiles, rewrites, templateCallsByFile, err := dynimport.ScanFiles(files)
	if err != nil {
		return nil, err
	}

	opts := api.BuildOptions{
		EntryPoints:   entryPoints,
		Bundle:        true,
		Metafile:      true,
		Write:         false,
		Platform:      api.PlatformNode,
		Packages:      api.PackagesExternal,
		LogLevel:      api.LogLevelSilent,
		AbsWorkingDir: dir,
		Outdir:        ".fastci-metafile",
	}
	if len(rewrites) > 0 {
		opts.Plugins = append(opts.Plugins, dynimport.NeutralizerPlugin(rewrites))
	}
	result := api.Build(opts)
	if len(result.Errors) > 0 {
		msgs := make([]string, len(result.Errors))
		for i, e := range result.Errors {
			msgs[i] = e.Text
		}
		return nil, fmt.Errorf("vitestanalyzer: esbuild: %s", strings.Join(msgs, "; "))
	}

	var mf metafile
	if err := json.Unmarshal([]byte(result.Metafile), &mf); err != nil {
		return nil, fmt.Errorf("vitestanalyzer: parsing esbuild metafile: %w", err)
	}

	toAbs := func(relPath string) string {
		return filepath.Join(dir, filepath.FromSlash(relPath))
	}

	for relPath, input := range mf.Inputs {
		abs := toAbs(relPath)
		n := g.Node(abs)
		n.Files = []string{abs}
		if isTestFile(abs) {
			n.HasTestFiles = true
		}
		for _, imp := range input.Imports {
			if imp.External {
				continue
			}
			impAbs := toAbs(imp.Path)
			if impAbs == abs {
				continue
			}
			n.Imports[impAbs] = true
		}
	}

	// Apply the dynamic-import safety net: opaque calls mark their node as
	// always possibly affected, and template calls with a static directory
	// prefix get real edges to every file under that directory (a safe
	// superset - esbuild itself never sees these calls, see the dynimport
	// package doc). A file is only left unmarked if every one of its
	// template calls resolved to real files.
	for absFile := range opaqueFiles {
		g.Node(absFile).HasDynamicImport = true
	}
	for absFile, calls := range templateCallsByFile {
		n := g.Node(absFile)
		anyUnmatched := false
		for _, call := range calls {
			dirPrefix := dynimport.TemplatePrefixDir(absFile, call.StaticPrefix) + string(filepath.Separator)
			matched := false
			for _, f := range files {
				if f != absFile && strings.HasPrefix(f, dirPrefix) {
					n.Imports[f] = true
					matched = true
				}
			}
			if !matched {
				anyUnmatched = true
			}
		}
		if anyUnmatched {
			n.HasDynamicImport = true
		}
	}

	g.IndexFiles()
	g.BuildImporters()
	return g, nil
}

// testFileSuffixRE matches Vitest's default include pattern:
// **/*.{test,spec}.?(c|m)[jt]s?(x). Custom test/include overrides in a
// vitest.config.* aren't honored yet - such a project still works, but
// test-file classification falls back to this default.
var testFileSuffixRE = regexp.MustCompile(`\.(test|spec)\.([cm]?[jt]sx?)$`)

func isTestFile(absPath string) bool {
	return testFileSuffixRE.MatchString(filepath.Base(absPath))
}

var fullRunBasenames = map[string]bool{
	"package.json": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"vitest.config.js": true, "vitest.config.ts": true, "vitest.config.mjs": true,
	"vitest.config.cjs": true, "vitest.config.mts": true, "vitest.config.cts": true,
	"vite.config.js": true, "vite.config.ts": true, "vite.config.mjs": true,
	"vite.config.cjs": true, "vite.config.mts": true, "vite.config.cts": true,
}

// FullRunFile reports whether a changed file should force a full test run:
// manifests, lockfiles, and Vite/Vitest config (which can alter aliases,
// plugins, or test settings in ways the import graph alone doesn't capture)
// all qualify.
func (*Analyzer) FullRunFile(absPath string) bool {
	base := filepath.Base(absPath)
	if fullRunBasenames[base] {
		return true
	}
	if strings.HasPrefix(base, "tsconfig") && strings.HasSuffix(base, ".json") {
		return true
	}
	return false
}

// Ignorable reports whether a changed file outside the tracked source set
// (docs, styles, JSON data, ambient .d.ts declarations, etc.) is safe to
// ignore.
func (*Analyzer) Ignorable(absPath string) bool {
	if strings.HasSuffix(absPath, ".d.ts") {
		return true
	}
	return !trackedExt[filepath.Ext(absPath)]
}

// AllTargets returns nil: `vitest run` with no path arguments already runs
// every discovered test, so a full/--all run needs no explicit target list.
func (*Analyzer) AllTargets(dir string) ([]string, error) {
	return nil, nil
}

// RunTests runs `vitest run` (the non-watch, single-pass mode - Vitest
// defaults to watch mode otherwise), using the local
// node_modules/.bin/vitest binary if present and falling back to `npx
// vitest` otherwise. Targets (test file paths) are passed as positional
// filter arguments, which Vitest matches directly against test file paths.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	argv := vitestBinArgv(dir)
	argv = append(argv, "run")
	argv = append(argv, targets...)
	argv = append(argv, extraArgs...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
}

func vitestBinArgv(dir string) []string {
	local := filepath.Join(dir, "node_modules", ".bin", "vitest")
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return []string{local}
	}
	return []string{"npx", "vitest"}
}
