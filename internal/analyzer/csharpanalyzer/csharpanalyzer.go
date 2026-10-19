// Package csharpanalyzer implements the fastci analyzer.Analyzer
// interface for .NET projects (a solution's .sln, or a standalone
// .csproj), at file granularity (like Jest/Vitest/pytest/Java).
//
// Import resolution delegates to the real language tooling, the same
// "don't reimplement what the toolchain already does" approach as
// go/packages for Go, cargo metadata for Rust, or FastciJavaParse.java's
// use of javac's own Compiler Tree API for Java: FastciCSharpParse.cs, a
// small shim embedded in this package, uses Roslyn (Microsoft.CodeAnalysis
// .CSharp) - the same compiler frontend `csc` itself uses - in parse-only
// mode (CSharpSyntaxTree.ParseText, never a Compilation/SemanticModel), so
// it needs nothing beyond the source tree itself: no project references,
// no NuGet restore, no successful build.
//
// Unlike the JDK, which bundles its Compiler Tree API as a public,
// documented API any `java <file>.java` single-file-source-launch program
// can use directly, the .NET SDK doesn't expose Roslyn as a package at
// all - Microsoft.CodeAnalysis.CSharp is normally a separate NuGet
// package. But Roslyn *is* already present on disk inside every .NET SDK
// install regardless, at <sdk>/Roslyn/bincore - it's how `csc.dll`/
// `vbc.dll` are implemented - just not NuGet-packaged or exposed as a
// single-file-launch target. This package compiles FastciCSharpParse.cs
// itself, once (cached under dir/.fastci-cache - see ensureCompiledShim),
// by invoking `dotnet exec <sdk>/Roslyn/bincore/csc.dll` directly against
// those same bundled assemblies plus the SDK's own bundled reference-
// assembly pack (Microsoft.NETCore.App.Ref) - verified directly, with
// network access blocked outright, that this compiles and runs with no
// restore step of any kind. The compiled shim is then run via `dotnet
// exec` per Build()/RunTests() call, the same shape as calling `java
// FastciJavaParse.java` for Java, just split into an explicit compile-once
// step first (C# has no equivalent of Java's JEP 330 single-file source
// launch).
//
// C#'s own import model shapes this differently than Java's: a plain
// "using Some.Namespace;" names a *namespace*, never a specific type
// (unlike Java's "import a.b.C;", which always names one type) - so the
// common case isn't "resolve one pinpoint import", it's "this file can
// reference anything in that namespace", handled by the same
// identifier-token check javaanalyzer already uses for a same-package
// reference or a wildcard import, except here it's the *primary*
// mechanism, not a fallback for an edge case. "using static Some.Type;"
// (and a "using X = Some.Type;" alias, which can name either a type or a
// namespace) still get resolved precisely, by exact fully-qualified-name
// match, the same as a Java single-type import. A C# file may declare any
// number of types, at any nesting depth, with no filename relationship to
// any of them at all (unlike Java, which at least requires a public type
// to match its filename) - every type, nested or not, is registered with
// its own correct fully-qualified name (reflection's own "Outer+Inner"
// notation for a nested type).
//
// "global using X;" (C# 10+, of any of the three forms above) applies to
// every file in the project without a local "using" of its own - collected
// from every file up front and applied to all of them, rather than only
// the one file that happens to declare it.
package csharpanalyzer

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cmc-labo/fastci/internal/debug"
	"github.com/cmc-labo/fastci/internal/graph"
	"github.com/cmc-labo/fastci/internal/runner"
)

// Analyzer is the .NET/C# implementation of analyzer.Analyzer.
type Analyzer struct{}

// New returns a C# analyzer.
func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) Name() string { return "csharp" }

// Detect looks for a .NET solution (*.sln) or a standalone project
// (*.csproj) directly in dir - for a solution, that's the directory the
// .sln lives in, the same place every other analyzer's own "run from the
// project root" convention already expects. A directory with more than
// one *.csproj and no .sln at all isn't specifically handled - `dotnet
// test` itself will refuse to pick one, the same as if this analyzer
// didn't narrow anything; see README.
func (*Analyzer) Detect(dir string) (bool, error) {
	for _, pattern := range []string{"*.sln", "*.csproj"} {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return false, err
		}
		if len(matches) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// skipDirs are directories never worth walking into: build output
// (bin/obj), VCS/editor metadata, and dependency trees that couldn't
// contain this project's own source anyway.
var skipDirs = map[string]bool{
	"bin": true, "obj": true, ".git": true, ".vs": true, "node_modules": true,
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

//go:embed FastciCSharpParse.cs
var parserSource []byte

// parsedType is one type FastciCSharpParse.cs found - see that file's doc
// comment for exactly what "namespace" and "fqcn" mean.
type parsedType struct {
	Namespace string `json:"namespace"`
	FQCN      string `json:"fqcn"`
}

// parsedGlobalUsing is one `global using` directive FastciCSharpParse.cs
// found - see that file's doc comment.
type parsedGlobalUsing struct {
	Kind   string `json:"kind"` // "plain", "static", or "alias"
	Target string `json:"target"`
}

// parsedCSFile is one file's entry in FastciCSharpParse.cs's own JSON
// output - see that file's doc comment.
type parsedCSFile struct {
	Types            []parsedType        `json:"types"`
	Usings           []string            `json:"usings"`
	DirectTypeRefs   []string            `json:"directTypeRefs"`
	GlobalUsings     []parsedGlobalUsing `json:"globalUsings"`
	Identifiers      []string            `json:"identifiers"`
	HasTestAttribute bool                `json:"hasTestAttribute"`
}

type parserOutput struct {
	Files map[string]parsedCSFile `json:"files"`
}

// roslynToolchain is everything needed to compile and run
// FastciCSharpParse.cs against one installed .NET SDK's own bundled
// Roslyn, found by locateRoslyn.
type roslynToolchain struct {
	bincoreDir string // <sdk>/Roslyn/bincore - has csc.dll, Microsoft.CodeAnalysis(.CSharp).dll.
	refDir     string // <dotnet root>/packs/Microsoft.NETCore.App.Ref/<ver>/ref/net<major>.0 - compile-time reference assemblies.
	major      int    // the SDK's major version, e.g. 8 - used for both the ref-pack lookup and the compiled shim's own runtimeconfig.json.
}

// locateRoslyn finds an installed .NET SDK that bundles Roslyn (every
// official SDK distribution does, at <sdk>/Roslyn/bincore - see the
// package doc) by asking `dotnet --list-sdks`, which prints "<version>
// [<path>]" lines; the highest-versioned entry that actually has a usable
// Roslyn/bincore *and* a matching Microsoft.NETCore.App.Ref pack (needed
// to compile against) wins.
func locateRoslyn() (roslynToolchain, error) {
	if _, err := exec.LookPath("dotnet"); err != nil {
		return roslynToolchain{}, fmt.Errorf("csharpanalyzer: dotnet not found on PATH (needed to parse C# source via Roslyn, the compiler frontend bundled with every .NET SDK - install the .NET SDK, e.g. from https://dotnet.microsoft.com/download)")
	}

	out, err := exec.Command(dotnetBin(), "--list-sdks").Output()
	if err != nil {
		return roslynToolchain{}, fmt.Errorf("csharpanalyzer: listing installed .NET SDKs: %w", err)
	}

	var best roslynToolchain
	var bestVer []int
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sp := strings.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		verStr := line[:sp]
		sdkRoot := strings.Trim(strings.TrimSpace(line[sp:]), "[]")

		ver := parseVersionInts(verStr)
		if len(ver) == 0 || (best.bincoreDir != "" && compareVersions(ver, bestVer) <= 0) {
			continue
		}

		bincore := filepath.Join(sdkRoot, verStr, "Roslyn", "bincore")
		if !fileExists(filepath.Join(bincore, "csc.dll")) {
			continue
		}
		dotnetRoot := filepath.Dir(sdkRoot) // sdkRoot is "<dotnet root>/sdk".
		refDir := findRefPack(dotnetRoot, ver[0])
		if refDir == "" {
			continue
		}

		best = roslynToolchain{bincoreDir: bincore, refDir: refDir, major: ver[0]}
		bestVer = ver
	}

	if best.bincoreDir == "" {
		return roslynToolchain{}, fmt.Errorf("csharpanalyzer: no installed .NET SDK has a usable bundled Roslyn compiler (checked `dotnet --list-sdks`'s own output for a <sdk>/Roslyn/bincore/csc.dll and a matching Microsoft.NETCore.App.Ref pack)")
	}
	return best, nil
}

// findRefPack finds the installed Microsoft.NETCore.App.Ref pack's
// reference-assembly directory for the given SDK major version - these
// are needed to compile FastciCSharpParse.cs itself (see
// ensureCompiledShim), and are always bundled alongside the SDK itself
// (packs/Microsoft.NETCore.App.Ref/<pack-version>/ref/net<major>.0/), even
// though the pack's own version number doesn't necessarily match the
// SDK's (e.g. SDK 8.0.131 ships ref pack 8.0.31) - only the TFM directory
// name actually matters here.
func findRefPack(dotnetRoot string, major int) string {
	pattern := filepath.Join(dotnetRoot, "packs", "Microsoft.NETCore.App.Ref", "*", "ref", fmt.Sprintf("net%d.0", major))
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// parseVersionInts splits a version string like "8.0.131" or
// "9.0.100-preview.1" into its leading numeric components.
func parseVersionInts(v string) []int {
	parts := strings.Split(v, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		digits := 0
		for digits < len(p) && p[digits] >= '0' && p[digits] <= '9' {
			digits++
		}
		if digits == 0 {
			break
		}
		n, _ := strconv.Atoi(p[:digits])
		nums = append(nums, n)
		if digits < len(p) {
			break // a non-digit suffix (e.g. "-preview") ends the numeric part.
		}
	}
	return nums
}

func compareVersions(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return len(a) - len(b)
}

// ensureCompiledShim compiles FastciCSharpParse.cs, once, into
// dir/.fastci-cache (reused across calls and across process invocations,
// keyed by a hash of the shim's own source plus the Roslyn toolchain
// path, so a fastci upgrade or a different installed SDK each get a fresh
// compile rather than silently reusing a stale one) and returns the
// compiled DLL's path, ready to run via `dotnet exec`.
func ensureCompiledShim(rt roslynToolchain, dir string) (string, error) {
	cacheRoot := filepath.Join(dir, ".fastci-cache")
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		return "", fmt.Errorf("csharpanalyzer: %w", err)
	}
	if gitignore := filepath.Join(cacheRoot, ".gitignore"); !fileExists(gitignore) {
		_ = os.WriteFile(gitignore, []byte("*\n"), 0o644)
	}

	h := sha256.Sum256(append(append([]byte(nil), parserSource...), []byte(rt.bincoreDir)...))
	key := hex.EncodeToString(h[:])[:16]
	outDir := filepath.Join(cacheRoot, "csharpparser-"+key)
	dllPath := filepath.Join(outDir, "FastciCSharpParse.dll")
	if fileExists(dllPath) {
		return dllPath, nil
	}

	tmpDir, err := os.MkdirTemp(cacheRoot, "csharpparser-build-*")
	if err != nil {
		return "", fmt.Errorf("csharpanalyzer: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	shimSrc := filepath.Join(tmpDir, "FastciCSharpParse.cs")
	if err := os.WriteFile(shimSrc, parserSource, 0o644); err != nil {
		return "", fmt.Errorf("csharpanalyzer: %w", err)
	}

	tmpDLL := filepath.Join(tmpDir, "FastciCSharpParse.dll")
	refMatches, _ := filepath.Glob(filepath.Join(rt.refDir, "*.dll"))
	argv := []string{dotnetBin(), "exec", filepath.Join(rt.bincoreDir, "csc.dll"), "-nologo", "-target:exe", "-out:" + tmpDLL}
	for _, f := range refMatches {
		argv = append(argv, "-r:"+f)
	}
	argv = append(argv,
		"-r:"+filepath.Join(rt.bincoreDir, "Microsoft.CodeAnalysis.dll"),
		"-r:"+filepath.Join(rt.bincoreDir, "Microsoft.CodeAnalysis.CSharp.dll"),
		shimSrc,
	)
	cmd := exec.Command(argv[0], argv[1:]...)
	done := debug.Command("csharpanalyzer", cmd.Args, "")
	out, err := cmd.CombinedOutput()
	done()
	if err != nil {
		return "", fmt.Errorf("csharpanalyzer: compiling the Roslyn parser shim: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	// The compiled shim needs Microsoft.CodeAnalysis(.CSharp).dll at
	// *runtime* too, not just compile time - the .NET runtime's own probing
	// only looks in the app's own directory and the shared framework, not
	// wherever csc.dll happened to be invoked from - so both are copied
	// alongside it (verified directly: running the compiled shim without
	// this fails with a FileNotFoundException for Microsoft.CodeAnalysis).
	for _, name := range []string{"Microsoft.CodeAnalysis.dll", "Microsoft.CodeAnalysis.CSharp.dll"} {
		if err := copyFile(filepath.Join(rt.bincoreDir, name), filepath.Join(tmpDir, name)); err != nil {
			return "", fmt.Errorf("csharpanalyzer: %w", err)
		}
	}

	rc := fmt.Sprintf(`{"runtimeOptions":{"tfm":"net%d.0","framework":{"name":"Microsoft.NETCore.App","version":"%d.0.0"}}}`, rt.major, rt.major)
	if err := os.WriteFile(filepath.Join(tmpDir, "FastciCSharpParse.runtimeconfig.json"), []byte(rc), 0o644); err != nil {
		return "", fmt.Errorf("csharpanalyzer: %w", err)
	}

	if fileExists(dllPath) {
		return dllPath, nil // a concurrent fastci run already finished compiling - reuse it.
	}
	if err := os.Rename(tmpDir, outDir); err != nil {
		if fileExists(dllPath) {
			return dllPath, nil // lost the rename race to a concurrent run - also fine.
		}
		return "", fmt.Errorf("csharpanalyzer: %w", err)
	}
	return dllPath, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// parseCSFiles runs FastciCSharpParse.cs (see the package doc) against
// every path, feeding the list via stdin (one per line) rather than argv
// to avoid any risk of an argv-length limit on a large project, and
// returns the parsed result keyed by absolute file path. dir is the
// project root, used only to place the compiled shim's own cache (see
// ensureCompiledShim) - it doesn't need to contain path.
func parseCSFiles(dir string, paths []string) (map[string]parsedCSFile, error) {
	if len(paths) == 0 {
		return map[string]parsedCSFile{}, nil
	}

	rt, err := locateRoslyn()
	if err != nil {
		return nil, err
	}
	dllPath, err := ensureCompiledShim(rt, dir)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(dotnetBin(), "exec", dllPath)
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n"))
	done := debug.Command("csharpanalyzer", cmd.Args, dir)
	out, err := cmd.Output()
	done()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
				return nil, fmt.Errorf("csharpanalyzer: parsing C# source: %w\n%s", err, stderr)
			}
		}
		return nil, fmt.Errorf("csharpanalyzer: parsing C# source: %w", err)
	}

	var parsed parserOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("csharpanalyzer: parsing FastciCSharpParse.cs's output: %w", err)
	}
	return parsed.Files, nil
}

// Build walks dir for every *.cs file, parses all of them in one shim
// invocation (see parseCSFiles), and builds a graph edge for every
// resolvable reference - see the package doc for exactly what
// "resolvable" means here.
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
	var allPaths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != dir && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".cs") {
			allPaths = append(allPaths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("csharpanalyzer: scanning %s: %w", dir, err)
	}

	files, err := parseCSFiles(dir, allPaths)
	if err != nil {
		return nil, err
	}

	// fqcnToFile resolves a direct type reference (a using-static target,
	// or a using-alias target that turns out to be a type) to the file
	// that declares it - every type a file declares is registered, at
	// every nesting depth, not just its top-level ones. pkgToFiles is
	// every file contributing at least one type to a given namespace, for
	// the same-namespace and plain-using token checks. globalPlain/
	// globalDirect are every project's own `global using` directives
	// (see the package doc), collected once and applied to every file
	// below, not just the one that declared them.
	fqcnToFile := make(map[string]string, len(files))
	pkgToFiles := make(map[string][]string, len(files))
	var globalPlain, globalDirect []string
	for path, pf := range files {
		seenNS := make(map[string]bool, len(pf.Types))
		for _, t := range pf.Types {
			fqcnToFile[t.FQCN] = path
			if !seenNS[t.Namespace] {
				seenNS[t.Namespace] = true
				pkgToFiles[t.Namespace] = append(pkgToFiles[t.Namespace], path)
			}
		}
		for _, gu := range pf.GlobalUsings {
			if gu.Kind == "plain" {
				globalPlain = append(globalPlain, gu.Target)
			} else {
				globalDirect = append(globalDirect, gu.Target)
			}
		}
	}

	// hasAnyType reports whether path declares a type whose simple name
	// (see simpleTypeName) is among idents - the shared token check both
	// the same-namespace and plain-using resolution below use.
	hasAnyType := func(path string, idents map[string]bool) bool {
		for _, t := range files[path].Types {
			if idents[simpleTypeName(t.FQCN)] {
				return true
			}
		}
		return false
	}

	g := graph.New()
	g.DisableDirFallback = true // one node per file - see package doc.

	for path, pf := range files {
		n := g.Node(path)
		n.Files = []string{path}
		if isTestFile(path, pf) {
			n.HasTestFiles = true
		}

		idents := make(map[string]bool, len(pf.Identifiers))
		for _, id := range pf.Identifiers {
			idents[id] = true
		}

		// A same-namespace reference needs no using directive at all in
		// real C#, the same as a same-package reference in Java - see the
		// package doc. A file can contribute types to more than one
		// namespace (several namespace blocks in one file), so this
		// checks every namespace the file's own types belong to.
		seenNS := make(map[string]bool, len(pf.Types))
		for _, t := range pf.Types {
			if seenNS[t.Namespace] {
				continue
			}
			seenNS[t.Namespace] = true
			for _, sibling := range pkgToFiles[t.Namespace] {
				if sibling != path && hasAnyType(sibling, idents) {
					n.Imports[sibling] = true
				}
			}
		}

		for _, fqcn := range append(append([]string(nil), pf.DirectTypeRefs...), globalDirect...) {
			if target, ok := fqcnToFile[fqcn]; ok && target != path {
				n.Imports[target] = true
			}
		}

		for _, ns := range append(append([]string(nil), pf.Usings...), globalPlain...) {
			for _, member := range pkgToFiles[ns] {
				if member != path && hasAnyType(member, idents) {
					n.Imports[member] = true
				}
			}
			// A plain "using X;" where X is actually a type, not a
			// namespace (true only via a using-alias target that didn't
			// resolve directly above) finds nothing here if X was never
			// registered as a namespace at all - harmless, since the
			// direct-type-reference pass above already covers that case.
		}
	}

	g.IndexFiles()
	g.BuildImporters()
	return g, nil
}

// simpleTypeName extracts a fqcn's own simple name: the part after the
// last "+" (a nested type) if any, else the part after the last "."
// (a namespace separator).
func simpleTypeName(fqcn string) string {
	if i := strings.LastIndexByte(fqcn, '+'); i >= 0 {
		return fqcn[i+1:]
	}
	if i := strings.LastIndexByte(fqcn, '.'); i >= 0 {
		return fqcn[i+1:]
	}
	return fqcn
}

// testNameRE matches the common real-world .NET test *naming* convention
// (Foo.Tests.cs / FooTests.cs / FooTest.cs) - used only as a secondary
// signal (see isTestFile) and to pick which type is "the test one" in a
// file declaring several (see classFQCNForTarget), since unlike Java/
// Maven Surefire, C# has no naming convention any test framework itself
// actually depends on at all: xUnit/NUnit/MSTest all discover tests by
// attribute, not filename - a fresh `dotnet new xunit` project's own
// default file is literally named "UnitTest1.cs", matching no naming
// convention whatsoever, which is exactly why isTestFile's real, primary
// signal is the parser's own hasTestAttribute instead.
var testNameRE = regexp.MustCompile(`(?i)(Test|Tests)\.cs$`)

// isTestFile reports whether a file is a test file: primarily by checking
// whether the parser found a recognized test-framework attribute anywhere
// in it (pf.HasTestAttribute), falling back to the naming convention
// (testNameRE) for the rare case of a test written without one of the
// attributes this recognizes (e.g. a custom in-house test framework).
func isTestFile(absPath string, pf parsedCSFile) bool {
	return pf.HasTestAttribute || testNameRE.MatchString(filepath.Base(absPath))
}

// fullRunExtensions are file extensions whose effect on the build
// (project references, package references, target framework) isn't
// captured by the import graph at all.
var fullRunExtensions = map[string]bool{
	".csproj": true, ".sln": true,
}

// fullRunBasenames are the same, for manifests with a fixed name instead
// of a per-project one.
var fullRunBasenames = map[string]bool{
	"Directory.Build.props": true, "Directory.Build.targets": true,
	"Directory.Packages.props": true, "nuget.config": true, "NuGet.Config": true,
}

// FullRunFile reports whether a changed file should force a full test
// run: any project/solution file or shared build manifest qualifies -
// see fullRunExtensions/fullRunBasenames.
func (*Analyzer) FullRunFile(absPath string) bool {
	if fullRunExtensions[filepath.Ext(absPath)] {
		return true
	}
	return fullRunBasenames[filepath.Base(absPath)]
}

// Ignorable reports whether a changed non-.cs file, or a file under
// bin/obj, is safe to ignore. The bin/obj check matters more here than
// the equivalent would for Java/Cargo: the .NET SDK unconditionally
// auto-generates real .cs files on *every* build (AssemblyInfo.cs,
// GlobalUsings.g.cs, an AssemblyAttributes.cs, ...) - without this check,
// those generated files have a ".cs" extension but don't resolve to any
// graph node (skipDirs already excludes bin/obj from Build's own walk),
// so every one of them looks like an unresolvable source change and
// forces a full run on every single build, for no real reason at all.
func (*Analyzer) Ignorable(absPath string) bool {
	if filepath.Ext(absPath) != ".cs" {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(absPath), "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

// AllTargets returns nil: `dotnet test` run with no --filter at all
// already discovers and runs everything - a full/--all run needs no
// explicit target list, the same as pytest/Java.
func (*Analyzer) AllTargets(dir string) ([]string, error) {
	return nil, nil
}

// RunTests runs `dotnet test`, selecting targets (absolute .cs file
// paths) by fully-qualified class name via VSTest's own --filter
// expression syntax. Unlike Maven Surefire and Gradle (both of which fail
// their entire build the moment a test-class filter matches zero classes
// in some *other* project in the solution - verified directly while
// building javaanalyzer's own multi-module support), `dotnet test` itself
// already reports a zero-match project as an informational "No test
// matches the given testcase filter" line and keeps going, with a zero
// exit code overall - verified directly against a real multi-project
// solution, so no equivalent workaround is needed here. An empty targets
// (a full/--all run) omits --filter entirely, letting `dotnet test`'s own
// default (discover and run everything) apply.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	argv := []string{dotnetBin(), "test"}
	if len(targets) > 0 {
		fqcns, err := classFQCNsForTargets(dir, targets)
		if err != nil {
			return err
		}
		if len(fqcns) > 0 {
			var clauses []string
			for _, fqcn := range fqcns {
				clauses = append(clauses, "FullyQualifiedName~"+fqcn)
			}
			argv = append(argv, "--filter", strings.Join(clauses, "|"))
		}
	}
	argv = append(argv, extraArgs...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
}

func dotnetBin() string {
	if path, err := exec.LookPath("dotnet"); err == nil {
		return path
	}
	return "dotnet"
}

// classFQCNsForTargets re-parses every target file (in one shim
// invocation) to find, per file, the type to filter on: the first one
// whose name matches the common test-naming convention (testNameRE),
// falling back to the first declared type at all if none match. The
// near-universal real-world convention of one test class per file makes
// this reliable in practice even though, unlike isTestFile, it can't use
// the attribute-based signal here - this picks one specific *type* out of
// possibly several in the file, and attribute detection only tells us the
// file as a whole has one somewhere, not which type it belongs to. Unlike
// javaanalyzer's own path-based fqcnFromPath, C# also has no standard
// source-root convention to derive this from the path alone. A target
// whose only declared type is nested (fqcn contains "+") is passed through
// as-is - VSTest's own --filter matching on a nested test class isn't
// specifically handled beyond that.
func classFQCNsForTargets(dir string, targets []string) ([]string, error) {
	files, err := parseCSFiles(dir, targets)
	if err != nil {
		return nil, err
	}
	var fqcns []string
	for _, t := range targets {
		pf, ok := files[t]
		if !ok || len(pf.Types) == 0 {
			continue // unparseable, or no type declared at all - omitted from the filter.
		}
		chosen := pf.Types[0]
		for _, ty := range pf.Types {
			if testNameRE.MatchString(simpleTypeName(ty.FQCN) + ".cs") {
				chosen = ty
				break
			}
		}
		fqcns = append(fqcns, chosen.FQCN)
	}
	return fqcns, nil
}
