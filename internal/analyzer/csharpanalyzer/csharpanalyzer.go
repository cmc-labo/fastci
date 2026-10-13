// Package csharpanalyzer implements the fastci analyzer.Analyzer
// interface for .NET projects (a solution's .sln, or a standalone
// .csproj), at file granularity (like Jest/Vitest/pytest/Java).
//
// This is a first, deliberately simple version, closer in spirit to
// javaanalyzer's original approach than its current one: a regexp-based
// text scan over each .cs file's own "namespace"/"using" declarations and
// identifier tokens, not a real parser. Roslyn (Microsoft.CodeAnalysis.
// CSharp), the JDK-for-javac equivalent for C#, isn't bundled with the
// .NET SDK itself the way the JDK's own Compiler Tree API ships inside
// every JDK - unlike javaanalyzer's embedded parser shim, a Roslyn-based
// one would need its own NuGet package restored (network access) before
// it could even run, which would be a real new precondition this
// analyzer specifically avoids for now. A real-parser upgrade remains a
// legitimate future direction if that trade-off ever becomes worth it -
// see javaanalyzer's own migration for the precedent - but isn't what a
// first, simple version needs.
//
// C#'s own import model shapes this differently than Java's regexp
// scanner did, not just translated: a plain "using Some.Namespace;"
// names a *namespace*, never a specific type (unlike Java's
// "import a.b.C;", which always names one type) - so the common case
// isn't "resolve one pinpoint import", it's "this file can reference
// anything in that namespace", handled by the same identifier-token
// check javaanalyzer already uses for a same-package reference or a
// wildcard import, except here it's the *primary* mechanism, not a
// fallback for an edge case. "using static Some.Type;" (and a
// "using X = Some.Type;" alias, which can name either a type or a
// namespace) still get resolved precisely, by exact fully-qualified-name
// match, the same as a Java single-type import. A C# file may declare
// any number of top-level types with no filename relationship to any of
// them at all (unlike Java, which at least requires a public type to
// match its filename) - every type is registered, not just one.
//
// "global using X;" (C# 10+) applies to every file in the project without
// a local "using" of its own - collected from every file up front and
// applied to all of them, rather than only the one file that happens to
// declare it.
package csharpanalyzer

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/runner"
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

type csFile struct {
	path string

	// namespace is the first namespace declaration found in the file -
	// block-scoped ("namespace X { ... }") or file-scoped ("namespace
	// X;", C# 10+) are both matched identically, since only the name
	// itself is needed. A file with more than one namespace block
	// (legal, but unusual and discouraged in modern C#) only has its
	// first one tracked.
	namespace string

	// types are every top-level (or nested - see the package doc's scope
	// note) type's simple name declared in the file: class/struct/
	// interface/record/enum.
	types []string

	// usings are plain "using X;" namespace imports.
	usings []string

	// directTypeRefs are every using that names one specific type rather
	// than a namespace: "using static X.Y;" (Y is dropped as a member
	// access the same way javaanalyzer drops a static-import's member
	// name) and "using Alias = X.Y;" (tried as a direct type match
	// first, falling back to namespace-style resolution - see Build).
	directTypeRefs []string

	// hasTestAttribute reports whether the file contains a recognized
	// test-framework attribute ([Fact]/[Theory] for xUnit, [Test]/
	// [TestCase] for NUnit, [TestMethod]/[DataTestMethod] for MSTest) -
	// see isTestFile for why this, not the filename, is the real signal
	// for C#.
	hasTestAttribute bool

	identifiers map[string]bool
}

var (
	namespaceRE   = regexp.MustCompile(`^\s*namespace\s+([\w.]+)`)
	typeDeclRE    = regexp.MustCompile(`^\s*(?:\w+\s+)*(?:class|struct|interface|record|enum)\s+(\w+)`)
	usingStaticRE = regexp.MustCompile(`^\s*(?:global\s+)?using\s+static\s+([\w.]+)\s*;`)
	usingAliasRE  = regexp.MustCompile(`^\s*(?:global\s+)?using\s+\w+\s*=\s*([\w.]+)\s*;`)
	usingPlainRE  = regexp.MustCompile(`^\s*(?:global\s+)?using\s+([\w.]+)\s*;`)
	globalUsingRE = regexp.MustCompile(`^\s*global\s+using\b`)
	identifierRE  = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

	// testAttributeRE matches a recognized test-framework attribute,
	// covering xUnit ([Fact]/[Theory]), NUnit ([Test]/[TestCase]), and
	// MSTest ([TestMethod]/[DataTestMethod]) - see isTestFile.
	testAttributeRE = regexp.MustCompile(`\[\s*(?:Fact|Theory|Test|TestCase|TestMethod|DataTestMethod)\b`)
)

// parseCSFile extracts path's namespace, declared types, using
// directives, and identifier tokens via a plain regex scan - see the
// package doc for why this isn't a real parser. Lines recognized as a
// namespace/using declaration are excluded from the identifier-token
// scan, so (unlike a naive whole-file scan) a using directive's own
// namespace path never leaks in as if it were a real code reference -
// the exact bug javaanalyzer's own regexp-based predecessor had to learn
// the hard way; this is designed to avoid it from the start.
func parseCSFile(path string) (csFile, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return csFile{}, nil, err
	}
	cf := csFile{path: path}
	var globalUsings []string
	var identLines []string

	for _, line := range strings.Split(string(data), "\n") {
		if testAttributeRE.MatchString(line) {
			cf.hasTestAttribute = true
		}
		if m := namespaceRE.FindStringSubmatch(line); m != nil {
			if cf.namespace == "" {
				cf.namespace = m[1]
			}
			continue
		}
		if m := typeDeclRE.FindStringSubmatch(line); m != nil {
			cf.types = append(cf.types, m[1])
			identLines = append(identLines, line) // the type's own name is a real token too.
			continue
		}
		if m := usingStaticRE.FindStringSubmatch(line); m != nil {
			cf.directTypeRefs = append(cf.directTypeRefs, m[1])
			if globalUsingRE.MatchString(line) {
				globalUsings = append(globalUsings, "static:"+m[1])
			}
			continue
		}
		if m := usingAliasRE.FindStringSubmatch(line); m != nil {
			cf.directTypeRefs = append(cf.directTypeRefs, m[1])
			cf.usings = append(cf.usings, m[1]) // tried both ways - see Build.
			if globalUsingRE.MatchString(line) {
				globalUsings = append(globalUsings, "alias:"+m[1])
			}
			continue
		}
		if m := usingPlainRE.FindStringSubmatch(line); m != nil {
			cf.usings = append(cf.usings, m[1])
			if globalUsingRE.MatchString(line) {
				globalUsings = append(globalUsings, "plain:"+m[1])
			}
			continue
		}
		identLines = append(identLines, line)
	}

	tokens := identifierRE.FindAllString(strings.Join(identLines, "\n"), -1)
	cf.identifiers = make(map[string]bool, len(tokens))
	for _, t := range tokens {
		cf.identifiers[t] = true
	}
	return cf, globalUsings, nil
}

// Build walks dir for every *.cs file, parses each (see parseCSFile), and
// builds a graph edge for every resolvable reference - see the package
// doc for exactly what "resolvable" means here.
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

	files := make(map[string]csFile, len(allPaths))
	var globalUsings []string
	for _, path := range allPaths {
		cf, gu, err := parseCSFile(path)
		if err != nil {
			continue // unreadable file - skip it, don't fail the whole scan.
		}
		files[path] = cf
		globalUsings = append(globalUsings, gu...)
	}

	// fqcnToFile resolves a direct type reference (a using-static target,
	// or a using-alias target that turns out to be a type) to the file
	// that declares it; pkgToFiles is every file in a given namespace,
	// for the same-namespace and plain-using token checks.
	fqcnToFile := make(map[string]string, len(files))
	pkgToFiles := make(map[string][]string, len(files))
	for path, cf := range files {
		pkgToFiles[cf.namespace] = append(pkgToFiles[cf.namespace], path)
		for _, t := range cf.types {
			fqcn := t
			if cf.namespace != "" {
				fqcn = cf.namespace + "." + t
			}
			fqcnToFile[fqcn] = path
		}
	}

	hasAnyType := func(path string, idents map[string]bool) bool {
		for _, t := range files[path].types {
			if idents[t] {
				return true
			}
		}
		return false
	}

	g := graph.New()
	g.DisableDirFallback = true // one node per file - see package doc.

	for path, cf := range files {
		n := g.Node(path)
		n.Files = []string{path}
		if isTestFile(path, cf) {
			n.HasTestFiles = true
		}

		// A same-namespace reference needs no using directive in real
		// C#, the same as a same-package reference in Java - see the
		// package doc.
		for _, sibling := range pkgToFiles[cf.namespace] {
			if sibling != path && hasAnyType(sibling, cf.identifiers) {
				n.Imports[sibling] = true
			}
		}

		for _, fqcn := range cf.directTypeRefs {
			if target, ok := fqcnToFile[fqcn]; ok && target != path {
				n.Imports[target] = true
			}
		}

		for _, ns := range append(append([]string(nil), cf.usings...), namespacesOf(globalUsings)...) {
			for _, member := range pkgToFiles[ns] {
				if member != path && hasAnyType(member, cf.identifiers) {
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

// namespacesOf extracts the plain ("plain:"-prefixed) namespace targets
// from the collected global-using list - a global "using static ...;" or
// "using X = ...;" is already folded into every file's own
// directTypeRefs/usings via its own per-file entry, so only the plain
// form needs applying project-wide here.
func namespacesOf(globalUsings []string) []string {
	var out []string
	for _, gu := range globalUsings {
		if ns, ok := strings.CutPrefix(gu, "plain:"); ok {
			out = append(out, ns)
		}
	}
	return out
}

// testNameRE matches the common real-world .NET test *naming*
// convention (Foo.Tests.cs / FooTests.cs / FooTest.cs) - used only as a
// secondary signal (see isTestFile) and to pick which type is "the test
// one" in a file declaring several (see classFQCNForTarget), since
// unlike Java/Maven Surefire, C# has no naming convention any test
// framework itself actually depends on at all: xUnit/NUnit/MSTest all
// discover tests by attribute, not filename - a fresh `dotnet new xunit`
// project's own default file is literally named "UnitTest1.cs", matching
// no naming convention whatsoever, which is exactly why isTestFile's
// real, primary signal is testAttributeRE instead.
var testNameRE = regexp.MustCompile(`(?i)(Test|Tests)\.cs$`)

// isTestFile reports whether a file is a test file: primarily by
// checking whether it contains a recognized test-framework attribute
// (cf.hasTestAttribute - see testAttributeRE), falling back to the
// naming convention (testNameRE) for the rare case of a test written
// without one of the attributes this recognizes (e.g. a custom
// in-house test framework).
func isTestFile(absPath string, cf csFile) bool {
	return cf.hasTestAttribute || testNameRE.MatchString(filepath.Base(absPath))
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

// Ignorable reports whether a changed non-C# file is safe to ignore.
// Ignorable reports whether a changed non-.cs file, or a file under
// bin/obj, is safe to ignore. The bin/obj check matters more here than
// the equivalent would for Java/Cargo: the .NET SDK unconditionally
// auto-generates real .cs files on *every* build (AssemblyInfo.cs,
// GlobalUsings.g.cs, an AssemblyAttributes.cs, ...) - found by a real
// end-to-end run in a repo without a .gitignore yet (not the norm for a
// real .NET project, which almost always has one, but a real
// misconfiguration worth guarding against directly): without this check,
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
// expression syntax. Unlike Maven Surefire and Gradle (both of which
// fail their entire build the moment a test-class filter matches zero
// classes in some *other* project in the solution - verified directly
// while building javaanalyzer's own multi-module support), `dotnet test`
// itself already reports a zero-match project as an informational "No
// test matches the given testcase filter" line and keeps going, with a
// zero exit code overall - verified directly against a real
// multi-project solution, so no equivalent workaround is needed here.
// An empty targets (a full/--all run) omits --filter entirely, letting
// `dotnet test`'s own default (discover and run everything) apply.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	argv := []string{dotnetBin(), "test"}
	if len(targets) > 0 {
		var clauses []string
		for _, t := range targets {
			fqcn, ok := classFQCNForTarget(t)
			if !ok {
				continue // unparseable - see classFQCNForTarget; falls through to omitting it from the filter.
			}
			clauses = append(clauses, "FullyQualifiedName~"+fqcn)
		}
		if len(clauses) > 0 {
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

// classFQCNForTarget re-parses absPath (a selected target file) to find
// its namespace and the declared type to filter on: the first one whose
// name matches the common test-naming convention (testNameRE), falling
// back to the first declared type at all if none match. The
// near-universal real-world convention of one test class per file makes
// this reliable in practice even though, unlike isTestFile, it can't use
// the attribute-based signal here - this picks one specific *type* out
// of possibly several in the file, and attribute detection only tells us
// the file as a whole has one somewhere, not which type it belongs to.
// Unlike javaanalyzer's own path-based fqcnFromPath, C# also has no
// standard source-root convention to derive this from the path alone.
func classFQCNForTarget(absPath string) (string, bool) {
	cf, _, err := parseCSFile(absPath)
	if err != nil || len(cf.types) == 0 {
		return "", false
	}
	name := cf.types[0]
	for _, t := range cf.types {
		if testNameRE.MatchString(t + ".cs") {
			name = t
			break
		}
	}
	if cf.namespace == "" {
		return name, true
	}
	return cf.namespace + "." + name, true
}
