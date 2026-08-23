// Package jestanalyzer implements the fastci analyzer.Analyzer interface
// for TypeScript/JavaScript projects tested with Jest.
//
// Unlike Go, where "package" is a natural test-able unit, Jest tests at
// file granularity, so every source file is its own graph.Node here.
// Import edges (relative imports, tsconfig `paths`/`baseUrl` aliases,
// dynamic import() calls with a static string argument, and bare
// node_modules specifiers) are resolved by actually running them through
// esbuild's real resolver rather than pattern-matching import statements
// as strings - the same "delegate to the real toolchain" approach
// goanalyzer uses via go/packages. A Jest `moduleNameMapper` config (read
// from jest.config.json or package.json's "jest" field) is additionally
// applied via an esbuild resolver plugin, so aliases defined only there
// (not in tsconfig) are tracked too. See the package doc for
// modulenamemapper.go and the README for what's still out of reach: import
// specifiers built from a runtime-computed (non-literal) expression can't
// be resolved by any static tool, esbuild included.
package jestanalyzer

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

	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/runner"
)

// Analyzer is the Jest implementation of analyzer.Analyzer.
type Analyzer struct{}

// New returns a Jest analyzer.
func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) Name() string { return "jest" }

var jestConfigFileNames = []string{
	"jest.config.js", "jest.config.ts", "jest.config.mjs", "jest.config.cjs", "jest.config.json",
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
	Jest            json.RawMessage   `json:"jest"`
}

// Detect reports whether dir has a package.json with Jest configured,
// either via a jest.config.* file, a "jest" key in package.json, or jest
// listed as a dependency.
func (*Analyzer) Detect(dir string) (bool, error) {
	pkg, err := readPackageJSON(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	if len(pkg.Jest) > 0 {
		return true, nil
	}
	if _, ok := pkg.Dependencies["jest"]; ok {
		return true, nil
	}
	if _, ok := pkg.DevDependencies["jest"]; ok {
		return true, nil
	}
	for _, name := range jestConfigFileNames {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true, nil
		}
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
		return nil, fmt.Errorf("jestanalyzer: parsing package.json: %w", err)
	}
	return &pkg, nil
}

var trackedExt = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
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
// aliases, and extension/index resolution are all handled by esbuild's
// real resolver. Bare specifiers that resolve into node_modules (including
// other packages in an npm/pnpm/yarn workspace) are treated as external
// and are not tracked as graph edges; see the package doc and README for
// the current monorepo limitation this implies.
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
	files, err := discoverSourceFiles(dir)
	if err != nil {
		return nil, fmt.Errorf("jestanalyzer: discovering source files: %w", err)
	}
	g := graph.New()
	if len(files) == 0 {
		return g, nil
	}

	entryPoints := make([]string, len(files))
	for i, f := range files {
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			return nil, fmt.Errorf("jestanalyzer: %w", err)
		}
		entryPoints[i] = filepath.ToSlash(rel)
	}

	mapperEntries, err := loadModuleNameMapper(dir)
	if err != nil {
		return nil, err
	}

	opaqueFiles, rewrites, templateCallsByFile, err := scanFilesForDynamicImports(files)
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
	if len(mapperEntries) > 0 {
		opts.Plugins = append(opts.Plugins, moduleNameMapperPlugin(dir, mapperEntries))
	}
	if len(rewrites) > 0 {
		opts.Plugins = append(opts.Plugins, dynamicImportNeutralizerPlugin(rewrites))
	}
	result := api.Build(opts)
	if len(result.Errors) > 0 {
		msgs := make([]string, len(result.Errors))
		for i, e := range result.Errors {
			msgs[i] = e.Text
		}
		return nil, fmt.Errorf("jestanalyzer: esbuild: %s", strings.Join(msgs, "; "))
	}

	var mf metafile
	if err := json.Unmarshal([]byte(result.Metafile), &mf); err != nil {
		return nil, fmt.Errorf("jestanalyzer: parsing esbuild metafile: %w", err)
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
	// superset - esbuild itself never sees these calls, see dynamicimport.go).
	for absFile := range opaqueFiles {
		g.Node(absFile).HasDynamicImport = true
	}
	for absFile, calls := range templateCallsByFile {
		n := g.Node(absFile)
		anyUnmatched := false
		for _, call := range calls {
			dirPrefix := templatePrefixDir(absFile, call.StaticPrefix) + string(filepath.Separator)
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

// testFileSuffixRE matches Jest's default testMatch conventions:
// *.test.{js,jsx,ts,tsx,mjs,cjs} or *.spec.{...}. Custom testMatch/testRegex
// overrides in a Jest config aren't honored yet - see README.
var testFileSuffixRE = regexp.MustCompile(`\.(test|spec)\.(jsx?|tsx?|mjs|cjs)$`)

func isTestFile(absPath string) bool {
	for _, part := range strings.Split(filepath.ToSlash(absPath), "/") {
		if part == "__tests__" {
			return true
		}
	}
	return testFileSuffixRE.MatchString(filepath.Base(absPath))
}

var fullRunBasenames = map[string]bool{
	"package.json": true, "package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"jest.config.js": true, "jest.config.ts": true, "jest.config.mjs": true, "jest.config.cjs": true, "jest.config.json": true,
	"babel.config.js": true, "babel.config.cjs": true, "babel.config.mjs": true, "babel.config.json": true,
	".babelrc": true, ".babelrc.js": true, ".babelrc.json": true, ".swcrc": true,
}

// FullRunFile reports whether a changed file should force a full test run:
// manifests, lockfiles, and compiler/test-runner configs can all change
// behavior in ways the import graph alone doesn't capture.
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

// AllTargets returns nil: Jest run with no path arguments already runs
// every discovered test, so a full/--all run needs no explicit target list.
func (*Analyzer) AllTargets(dir string) ([]string, error) {
	return nil, nil
}

// RunTests runs Jest, using the local node_modules/.bin/jest binary if
// present and falling back to `npx jest` otherwise. When targets is
// non-empty, they're combined into a single anchored regex passed as a
// positional pattern argument - Jest matches positional arguments against
// each test file's absolute path. A single combined argument (rather than
// a --testPathPattern/--testPathPatterns flag, whose name changed between
// Jest 29 and 30) keeps this working across Jest versions.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	argv := jestBinArgv(dir)
	if len(targets) > 0 {
		argv = append(argv, testPathPattern(targets))
	}
	argv = append(argv, extraArgs...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
}

func jestBinArgv(dir string) []string {
	local := filepath.Join(dir, "node_modules", ".bin", "jest")
	if info, err := os.Stat(local); err == nil && !info.IsDir() {
		return []string{local}
	}
	return []string{"npx", "jest"}
}

func testPathPattern(targets []string) string {
	alts := make([]string, len(targets))
	for i, t := range targets {
		alts[i] = "^" + regexp.QuoteMeta(filepath.ToSlash(t)) + "$"
	}
	return strings.Join(alts, "|")
}
