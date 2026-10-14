package csharpanalyzer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/csharpanalyzer"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectSolution(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Demo.sln"), "")
	ok, err := csharpanalyzer.New().Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Detect = false with a .sln present, want true")
	}
}

func TestDetectStandaloneProject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	ok, err := csharpanalyzer.New().Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Detect = false with a .csproj present, want true")
	}
}

func TestDetectRejectsNonDotNetDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	ok, err := csharpanalyzer.New().Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Detect = true for a Go directory, want false")
	}
}

// TestBuildSameNamespaceEdgeNeedsNoUsing reproduces real C# semantics: a
// same-namespace reference needs no `using` directive at all, so Leaf
// and Consumer (both namespace Example, no using between them) must
// still be linked.
func TestBuildSameNamespaceEdgeNeedsNoUsing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Leaf.cs"), `namespace Example;

public class Leaf
{
    public static int Value() => 1;
}
`)
	writeFile(t, filepath.Join(dir, "Consumer.cs"), `namespace Example;

public class Consumer
{
    public static int Use() => Leaf.Value();
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "Leaf.cs")
	consumer := filepath.Join(dir, "Consumer.cs")
	if !g.Nodes[consumer].Imports[leaf] {
		t.Errorf("Consumer.cs's Imports = %v, want it to include Leaf.cs via same-namespace visibility", g.Nodes[consumer].Imports)
	}
}

// TestBuildPlainUsingResolvesViaTokenMatch covers the common C# case: a
// plain "using Other;" names a namespace, not one specific type, so
// resolution is a token check against that namespace's members - Used is
// actually referenced and must get an edge; Unused (same namespace,
// never mentioned) must not.
func TestBuildPlainUsingResolvesViaTokenMatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Other", "Used.cs"), `namespace Other;

public class Used
{
    public static int Value() => 1;
}
`)
	writeFile(t, filepath.Join(dir, "Other", "Unused.cs"), `namespace Other;

public class Unused
{
}
`)
	writeFile(t, filepath.Join(dir, "Consumer.cs"), `using Other;

namespace Example;

public class Consumer
{
    public static int Use() => Used.Value();
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	used := filepath.Join(dir, "Other", "Used.cs")
	unused := filepath.Join(dir, "Other", "Unused.cs")
	consumer := filepath.Join(dir, "Consumer.cs")
	if !g.Nodes[consumer].Imports[used] {
		t.Errorf("Consumer.cs's Imports = %v, want it to include Used.cs (actually referenced)", g.Nodes[consumer].Imports)
	}
	if g.Nodes[consumer].Imports[unused] {
		t.Errorf("Consumer.cs's Imports = %v, want it to NOT include Unused.cs (never referenced)", g.Nodes[consumer].Imports)
	}
}

// TestBuildUsingStaticResolvesExactType covers "using static X.Y;" -
// unlike a plain using, this names one specific type, so it should
// resolve precisely regardless of whether Y's name is actually
// referenced as a token (the member access through it, e.g. Y.Member(),
// is enough - the type name itself need not appear).
func TestBuildUsingStaticResolvesExactType(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Constants.cs"), `namespace Example;

public static class Constants
{
    public const int Foo = 1;
}
`)
	writeFile(t, filepath.Join(dir, "User.cs"), `using static Example.Constants;

namespace Other;

public class User
{
    public static int Use() => Foo;
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	constants := filepath.Join(dir, "Constants.cs")
	user := filepath.Join(dir, "User.cs")
	if !g.Nodes[user].Imports[constants] {
		t.Errorf("User.cs's Imports = %v, want it to include Constants.cs via its using-static directive", g.Nodes[user].Imports)
	}
}

// TestBuildExternalUsingIsNotAnEdge reproduces the common case: a using
// directive for a namespace that isn't part of this project at all
// (the BCL, a NuGet package) must be silently ignored, not treated as an
// unresolvable edge.
func TestBuildExternalUsingIsNotAnEdge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Thing.cs"), `using System;
using System.Collections.Generic;
using Xunit;

namespace Example;

public class Thing
{
    List<string> items = new List<string>();
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	thing := filepath.Join(dir, "Thing.cs")
	if len(g.Nodes[thing].Imports) != 0 {
		t.Errorf("Imports = %v, want none - System/System.Collections.Generic/Xunit aren't part of this project", g.Nodes[thing].Imports)
	}
}

// TestBuildGlobalUsingAppliesToEveryFile reproduces modern C# (10+):
// a "global using" declared in just one file (conventionally a
// GlobalUsings.cs) applies to every file in the project, with no local
// using needed anywhere else.
func TestBuildGlobalUsingAppliesToEveryFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "GlobalUsings.cs"), "global using Other;\n")
	writeFile(t, filepath.Join(dir, "Other", "Used.cs"), `namespace Other;

public class Used
{
    public static int Value() => 1;
}
`)
	writeFile(t, filepath.Join(dir, "Consumer.cs"), `namespace Example;

public class Consumer
{
    public static int Use() => Used.Value();
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	used := filepath.Join(dir, "Other", "Used.cs")
	consumer := filepath.Join(dir, "Consumer.cs")
	if !g.Nodes[consumer].Imports[used] {
		t.Errorf("Consumer.cs's Imports = %v, want it to include Used.cs via the project-wide global using (no local using needed)", g.Nodes[consumer].Imports)
	}
}

// TestBuildHandlesMultipleTypesInOneFile covers a routine C# pattern with
// no Java equivalent: several top-level types in one file, with no
// naming relationship to the filename or each other at all.
func TestBuildHandlesMultipleTypesInOneFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Multi.cs"), `namespace Example;

public class Primary
{
    public static int Value() => 1;
}

internal class Secondary
{
    public static int Compute() => 2;
}
`)
	writeFile(t, filepath.Join(dir, "User.cs"), `namespace Example;

public class User
{
    public static int Use() => Secondary.Compute();
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	multi := filepath.Join(dir, "Multi.cs")
	user := filepath.Join(dir, "User.cs")
	if !g.Nodes[user].Imports[multi] {
		t.Errorf("User.cs's Imports = %v, want it to include Multi.cs via Secondary (its second top-level type)", g.Nodes[user].Imports)
	}
}

func TestBuildMarksTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	for _, name := range []string{"FooTests.cs", "FooTest.cs", "Foo.Tests.cs"} {
		writeFile(t, filepath.Join(dir, name), "namespace Example;\nclass X {}\n")
	}
	writeFile(t, filepath.Join(dir, "Plain.cs"), "namespace Example;\nclass Plain {}\n")

	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"FooTests.cs", "FooTest.cs", "Foo.Tests.cs"} {
		path := filepath.Join(dir, name)
		if !g.Nodes[path].HasTestFiles {
			t.Errorf("%s: HasTestFiles = false, want true", name)
		}
	}
	plain := filepath.Join(dir, "Plain.cs")
	if g.Nodes[plain].HasTestFiles {
		t.Error("Plain.cs: HasTestFiles = true, want false")
	}
}

// TestBuildMarksTestFilesByAttributeRegardlessOfName is a regression test
// for a real bug found via a real end-to-end run against an actual
// `dotnet new xunit` project: its own generated default test file is
// named "UnitTest1.cs", matching no filename convention at all (unlike
// Java/Maven Surefire, no .NET test framework depends on file naming -
// they all discover tests by attribute). Without detecting the [Fact]
// attribute itself, a freshly-scaffolded .NET test project's own default
// file would never be recognized as a test at all.
func TestBuildMarksTestFilesByAttributeRegardlessOfName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "UnitTest1.cs"), `using Xunit;

namespace Example;

public class CalcTests
{
    [Fact]
    public void AddWorks()
    {
        Assert.Equal(5, 2 + 3);
    }
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "UnitTest1.cs")
	if !g.Nodes[path].HasTestFiles {
		t.Error("UnitTest1.cs (containing [Fact], dotnet new xunit's own default name): HasTestFiles = false, want true")
	}
}

func TestBuildSkipsBuildOutputDirectories(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Real.cs"), "namespace Example;\nclass Real {}\n")
	writeFile(t, filepath.Join(dir, "bin", "Debug", "net8.0", "Real.cs"), "namespace Example;\nclass Real {}\n")
	writeFile(t, filepath.Join(dir, "obj", "Real.cs"), "namespace Example;\nclass Real {}\n")

	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	for id := range g.Nodes {
		if hasPathComponent(id, "bin") || hasPathComponent(id, "obj") {
			t.Errorf("node %q was found under bin/obj, want it skipped entirely", id)
		}
	}
}

func hasPathComponent(path, part string) bool {
	for _, p := range strings.Split(filepath.ToSlash(path), "/") {
		if p == part {
			return true
		}
	}
	return false
}

func TestFullRunFile(t *testing.T) {
	a := csharpanalyzer.New()
	for _, name := range []string{"App.csproj", "Demo.sln", "Directory.Build.props", "Directory.Build.targets", "Directory.Packages.props", "nuget.config", "NuGet.Config"} {
		if !a.FullRunFile("/repo/" + name) {
			t.Errorf("FullRunFile(%q) = false, want true", name)
		}
	}
	if a.FullRunFile("/repo/Program.cs") {
		t.Error("FullRunFile for an ordinary source file = true, want false")
	}
}

func TestIgnorable(t *testing.T) {
	a := csharpanalyzer.New()
	if !a.Ignorable("/repo/README.md") {
		t.Error("Ignorable(README.md) = false, want true")
	}
	if a.Ignorable("/repo/Program.cs") {
		t.Error("Ignorable(Program.cs) = true, want false")
	}
}

// TestIgnorableSkipsSDKGeneratedFilesUnderObj is a regression test for a
// real bug found via a real end-to-end `dotnet build` run: the .NET SDK
// unconditionally generates real .cs files under obj/ on every build
// (AssemblyInfo.cs, GlobalUsings.g.cs, ...). Without this check, each one
// looks like an unresolvable real source change (it has a .cs extension,
// but Build's own skipDirs means it was never registered as a graph
// node), forcing a full run on every single build for no real reason.
func TestIgnorableSkipsSDKGeneratedFilesUnderObj(t *testing.T) {
	a := csharpanalyzer.New()
	for _, p := range []string{
		"/repo/MyApp/obj/Debug/net8.0/MyApp.AssemblyInfo.cs",
		"/repo/MyApp/obj/Debug/net8.0/MyApp.GlobalUsings.g.cs",
		"/repo/MyApp/bin/Debug/net8.0/SomeCopiedFile.cs",
	} {
		if !a.Ignorable(p) {
			t.Errorf("Ignorable(%q) = false, want true (SDK-generated, under bin/obj)", p)
		}
	}
}

// TestBuildResolvesNestedType is a regression test for a real precision
// gain the Roslyn migration unlocks: a nested type gets its own correct
// fully-qualified name (reflection's own "Outer+Inner" notation), so a
// same-namespace reference to it resolves exactly like any other type -
// the old regexp-based version couldn't distinguish a nested type from a
// top-level one at all.
func TestBuildResolvesNestedType(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Outer.cs"), `namespace Example;

public class Outer
{
    public class Inner
    {
        public static int Value() => 1;
    }
}
`)
	writeFile(t, filepath.Join(dir, "Consumer.cs"), `namespace Example;

public class Consumer
{
    public static int Use() => Outer.Inner.Value();
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	outer := filepath.Join(dir, "Outer.cs")
	consumer := filepath.Join(dir, "Consumer.cs")
	if !g.Nodes[consumer].Imports[outer] {
		t.Errorf("Consumer.cs's Imports = %v, want it to include Outer.cs via its nested Inner type", g.Nodes[consumer].Imports)
	}
}

// TestBuildHandlesMultipleNamespacesInOneFile is a regression test for
// another real precision gain: a file declaring more than one namespace
// block (legal, if unusual, C#) now has each of its types correctly
// attributed to its own enclosing namespace - the old regexp-based
// version only ever tracked a single "first namespace found" per file.
func TestBuildHandlesMultipleNamespacesInOneFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Multi.cs"), `namespace First
{
    public class FirstThing
    {
        public static int Value() => 1;
    }
}

namespace Second
{
    public class SecondThing
    {
    }
}
`)
	writeFile(t, filepath.Join(dir, "Consumer.cs"), `namespace First;

public class Consumer
{
    public static int Use() => FirstThing.Value();
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	multi := filepath.Join(dir, "Multi.cs")
	consumer := filepath.Join(dir, "Consumer.cs")
	if !g.Nodes[consumer].Imports[multi] {
		t.Errorf("Consumer.cs's Imports = %v, want it to include Multi.cs via FirstThing (in the matching `First` namespace block)", g.Nodes[consumer].Imports)
	}
}

// TestBuildGlobalUsingStaticAppliesToEveryFile is a regression test for a
// real gap the old regexp-based version had: it only ever folded a
// `global using static`/`global using Alias = ...` into the *declaring*
// file's own resolution, never applying it project-wide the way a plain
// `global using` already did - even though "global" means project-wide
// for all three forms equally in real C#.
func TestBuildGlobalUsingStaticAppliesToEveryFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "GlobalUsings.cs"), "global using static Example.Constants;\n")
	writeFile(t, filepath.Join(dir, "Constants.cs"), `namespace Example;

public static class Constants
{
    public const int Foo = 1;
}
`)
	writeFile(t, filepath.Join(dir, "User.cs"), `namespace Other;

public class User
{
    public static int Use() => Foo;
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	constants := filepath.Join(dir, "Constants.cs")
	user := filepath.Join(dir, "User.cs")
	if !g.Nodes[user].Imports[constants] {
		t.Errorf("User.cs's Imports = %v, want it to include Constants.cs via the project-wide global using-static (no local using needed)", g.Nodes[user].Imports)
	}
}

// TestBuildIgnoresUsingLookingTextInCommentOrString is a regression test
// for a real precision gain a real parser gives over a text-based regex
// scan: a commented-out or string-literal "using ...;"/"namespace ...;"
// is correctly never seen as a real declaration at all.
func TestBuildIgnoresUsingLookingTextInCommentOrString(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Other", "Used.cs"), `namespace Other;

public class Used
{
}
`)
	writeFile(t, filepath.Join(dir, "Thing.cs"), `// using Other;
namespace Example;

public class Thing
{
    string note = "using Other; namespace Other;";
}
`)
	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	used := filepath.Join(dir, "Other", "Used.cs")
	thing := filepath.Join(dir, "Thing.cs")
	if g.Nodes[thing].Imports[used] {
		t.Errorf("Thing.cs's Imports = %v, want it to NOT include Used.cs - the only mentions of \"using Other\" are in a comment and a string literal", g.Nodes[thing].Imports)
	}
}

// TestBuildDropsFileWithSyntaxError is a regression test matching
// FastciJavaParse.java's own "skip it, don't guess" conservatism: a file
// with a genuine syntax error is dropped from the parser's own output
// entirely, rather than risk building graph edges from a broken recovery
// tree - Build itself must still succeed (impact.Compute's own "unresolved
// source file" safety net is what actually reacts to this, not a hard
// error here).
func TestBuildDropsFileWithSyntaxError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Good.cs"), "namespace Example;\npublic class Good {}\n")
	writeFile(t, filepath.Join(dir, "Broken.cs"), "namespace Example {\n  public class Broken {\n    public void M( {\n  }\n")

	g, err := csharpanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "Good.cs")
	broken := filepath.Join(dir, "Broken.cs")
	if g.Nodes[good] == nil {
		t.Error("Good.cs: expected a graph node, got none")
	}
	if _, ok := g.Nodes[broken]; ok {
		t.Error("Broken.cs: expected no graph node for a file with a real syntax error, got one")
	}
}

// TestBuildReusesCompiledShimAcrossCalls exercises the cache-reuse path
// in ensureCompiledShim (a second Build call against the same project
// directory must find the already-compiled shim under .fastci-cache and
// skip recompiling it) - a correctness check (the second call must still
// produce the right graph), not a timing assertion.
func TestBuildReusesCompiledShimAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "App.csproj"), "<Project></Project>")
	writeFile(t, filepath.Join(dir, "Leaf.cs"), "namespace Example;\npublic class Leaf { public static int Value() => 1; }\n")
	writeFile(t, filepath.Join(dir, "Consumer.cs"), "namespace Example;\npublic class Consumer { public static int Use() => Leaf.Value(); }\n")

	a := csharpanalyzer.New()
	if _, err := a.Build(dir); err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(dir, ".fastci-cache")
	entries, err := os.ReadDir(cacheRoot)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected a compiled shim cached under %s after the first Build call, got entries=%v err=%v", cacheRoot, entries, err)
	}

	g, err := a.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "Leaf.cs")
	consumer := filepath.Join(dir, "Consumer.cs")
	if !g.Nodes[consumer].Imports[leaf] {
		t.Errorf("second Build call: Consumer.cs's Imports = %v, want it to include Leaf.cs", g.Nodes[consumer].Imports)
	}
}

func TestAllTargetsReturnsNil(t *testing.T) {
	a := csharpanalyzer.New()
	targets, err := a.AllTargets(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if targets != nil {
		t.Errorf("AllTargets = %v, want nil (dotnet test already discovers everything with no --filter)", targets)
	}
}
