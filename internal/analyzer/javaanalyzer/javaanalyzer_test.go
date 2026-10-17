package javaanalyzer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/javaanalyzer"
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

func TestDetectMaven(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	ok, err := javaanalyzer.New().Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Detect = false with pom.xml present, want true")
	}
}

func TestDetectGradleGroovy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.gradle"), "plugins { id 'java' }")
	ok, err := javaanalyzer.New().Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Detect = false with build.gradle present, want true")
	}
}

func TestDetectGradleKotlin(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "build.gradle.kts"), `plugins { java }`)
	ok, err := javaanalyzer.New().Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Detect = false with build.gradle.kts present, want true")
	}
}

func TestDetectRejectsNonJavaDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	ok, err := javaanalyzer.New().Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Detect = true for a Go directory, want false")
	}
}

// TestBuildSamePackageEdgeNeedsNoImport reproduces real Java semantics: a
// same-package reference needs no import statement at all, so Leaf and
// Consumer (both package com.example, no import between them) must still
// be linked - without this, javaanalyzer would silently miss the single
// most common kind of intra-project dependency in idiomatic Java code.
func TestBuildSamePackageEdgeNeedsNoImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Leaf.java"), `package com.example;

public class Leaf {
    public static int value() { return 1; }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Consumer.java"), `package com.example;

public class Consumer {
    public static int use() { return Leaf.value(); }
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "src/main/java/com/example/Leaf.java")
	consumer := filepath.Join(dir, "src/main/java/com/example/Consumer.java")
	if !g.Nodes[consumer].Imports[leaf] {
		t.Errorf("Consumer.java's Imports = %v, want it to include Leaf.java via same-package visibility (no import statement needed)", g.Nodes[consumer].Imports)
	}
}

// TestBuildCrossPackageImportResolves covers the normal case: an explicit
// single-type import across packages resolves to the real file.
func TestBuildCrossPackageImportResolves(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/util/Leaf.java"), `package com.example.util;

public class Leaf {
    public static int value() { return 1; }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/app/Consumer.java"), `package com.example.app;

import com.example.util.Leaf;

public class Consumer {
    public static int use() { return Leaf.value(); }
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "src/main/java/com/example/util/Leaf.java")
	consumer := filepath.Join(dir, "src/main/java/com/example/app/Consumer.java")
	if !g.Nodes[consumer].Imports[leaf] {
		t.Errorf("Consumer.java's Imports = %v, want it to include Leaf.java via its explicit cross-package import", g.Nodes[consumer].Imports)
	}
}

// TestBuildExternalImportIsNotAnEdge reproduces the overwhelmingly common
// case: an import of a JDK or third-party class (never declared anywhere
// in this project) must be silently ignored, not treated as an
// unresolvable/dynamic edge - the same treatment a node_modules import
// gets for Jest/Vitest.
func TestBuildExternalImportIsNotAnEdge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Thing.java"), `package com.example;

import java.util.List;
import org.junit.Test;

public class Thing {
    List<String> items;
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	thing := filepath.Join(dir, "src/main/java/com/example/Thing.java")
	n := g.Nodes[thing]
	if n.HasDynamicImport {
		t.Error("HasDynamicImport = true for ordinary external imports, want false")
	}
	if len(n.Imports) != 0 {
		t.Errorf("Imports = %v, want none - java.util.List/org.junit.Test aren't part of this project", n.Imports)
	}
}

// TestBuildWildcardImportResolvesUsedMembersOnly covers "import a.b.*;"
// against another of this project's own packages: Thing actually
// references Used (its name appears as a real token), so that edge must
// be tracked, but Thing never mentions Unused at all, so no edge to it
// should be added just because the wildcard could have reached it -
// token matching, not a blanket "wildcard touches everything in that
// package" edge.
func TestBuildWildcardImportResolvesUsedMembersOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/other/Used.java"), `package com.example.other;

public class Used {
    public static int value() { return 1; }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/other/Unused.java"), `package com.example.other;

public class Unused {}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Thing.java"), `package com.example;

import com.example.other.*;

public class Thing {
    int x = Used.value();
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	thing := filepath.Join(dir, "src/main/java/com/example/Thing.java")
	used := filepath.Join(dir, "src/main/java/com/example/other/Used.java")
	unused := filepath.Join(dir, "src/main/java/com/example/other/Unused.java")
	if !g.Nodes[thing].Imports[used] {
		t.Errorf("Thing.java's Imports = %v, want it to include Used.java (actually referenced)", g.Nodes[thing].Imports)
	}
	if g.Nodes[thing].Imports[unused] {
		t.Errorf("Thing.java's Imports = %v, want it to NOT include Unused.java (never referenced, despite the wildcard import)", g.Nodes[thing].Imports)
	}
	if g.Nodes[thing].HasDynamicImport {
		t.Error("HasDynamicImport = true, want false - a wildcard import is resolved by token matching here, not treated as unresolvable")
	}
}

// TestBuildSamePackageDoesNotLinkUnrelatedFiles is the regression case
// found by a real end-to-end run while building this analyzer: Calc and
// Formatter share a package and Formatter really does reference Calc
// (no import needed), but Unrelated also shares that package without
// ever mentioning Calc at all - an unconditional "same package = edge"
// rule would wrongly link it too, defeating impact analysis entirely for
// any project where tests share a package with the code under test (the
// normal Maven/Gradle src/main+src/test layout).
func TestBuildSamePackageDoesNotLinkUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Calc.java"), `package com.example;

public class Calc {
    public static int add(int a, int b) { return a + b; }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Formatter.java"), `package com.example;

public class Formatter {
    public static String format(int x) { return "v=" + Calc.add(x, 0); }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Unrelated.java"), `package com.example;

public class Unrelated {
    public static boolean ok() { return true; }
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	calc := filepath.Join(dir, "src/main/java/com/example/Calc.java")
	formatter := filepath.Join(dir, "src/main/java/com/example/Formatter.java")
	unrelated := filepath.Join(dir, "src/main/java/com/example/Unrelated.java")
	if !g.Nodes[formatter].Imports[calc] {
		t.Errorf("Formatter.java's Imports = %v, want it to include Calc.java (actually referenced)", g.Nodes[formatter].Imports)
	}
	if g.Nodes[unrelated].Imports[calc] {
		t.Errorf("Unrelated.java's Imports = %v, want it to NOT include Calc.java - Unrelated never mentions Calc", g.Nodes[unrelated].Imports)
	}
}

// TestBuildStaticImportResolvesToItsClass covers "import static a.b.C.X;"
// - the class is a.b.C, not a.b.C.X (X is a member, not a type), so
// resolution must drop that last segment before matching.
func TestBuildStaticImportResolvesToItsClass(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Constants.java"), `package com.example;

public class Constants {
    public static final int FOO = 1;
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/other/User.java"), `package com.example.other;

import static com.example.Constants.FOO;

public class User {
    int x = FOO;
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	constants := filepath.Join(dir, "src/main/java/com/example/Constants.java")
	user := filepath.Join(dir, "src/main/java/com/example/other/User.java")
	if !g.Nodes[user].Imports[constants] {
		t.Errorf("User.java's Imports = %v, want it to include Constants.java via its static import", g.Nodes[user].Imports)
	}
}

// TestBuildIgnoresCommentedOutImport is a regression test for a real bug
// the old regexp-based scanner had and the real javac-parser-based one
// fixes: a line-by-line regex has no notion of a block comment, so
// "/* import com.example.other.Foo; */" was indistinguishable from a
// real import. User.java here has no real import or reference to Foo at
// all - only this commented-out one - so no edge should exist.
func TestBuildIgnoresCommentedOutImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/other/Foo.java"), `package com.example.other;

public class Foo {
    public static int value() { return 1; }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/User.java"), `package com.example;

/*
import com.example.other.Foo;
*/

public class User {
    public static int use() { return 0; }
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	foo := filepath.Join(dir, "src/main/java/com/example/other/Foo.java")
	user := filepath.Join(dir, "src/main/java/com/example/User.java")
	if g.Nodes[user].Imports[foo] {
		t.Errorf("User.java's Imports = %v, want it to NOT include Foo.java - the only \"import\" is commented out", g.Nodes[user].Imports)
	}
}

// TestBuildIgnoresImportLookingTextInStringLiteral is the same class of
// regression as TestBuildIgnoresCommentedOutImport, for a string literal
// instead of a comment - equally invisible to a real parser, equally
// indistinguishable from a real import to a text-based regex scan.
func TestBuildIgnoresImportLookingTextInStringLiteral(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/other/Foo.java"), `package com.example.other;

public class Foo {
    public static int value() { return 1; }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/User.java"), `package com.example;

public class User {
    public static String use() {
        return "import com.example.other.Foo;";
    }
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	foo := filepath.Join(dir, "src/main/java/com/example/other/Foo.java")
	user := filepath.Join(dir, "src/main/java/com/example/User.java")
	if g.Nodes[user].Imports[foo] {
		t.Errorf("User.java's Imports = %v, want it to NOT include Foo.java - the only mention of it is inside a string literal", g.Nodes[user].Imports)
	}
}

// TestBuildHandlesMultipleTopLevelTypesInOneFile is a regression test for
// the other real limitation the old filename-matching assumption had: a
// single file can legally declare more than one top-level type. Both
// Primary (public) and Secondary (package-private) live in Multi.java;
// User.java references Secondary (legally, via same-package visibility,
// with no import) and must resolve to Multi.java, not go unresolved
// because Secondary's name doesn't match its file's name.
func TestBuildHandlesMultipleTopLevelTypesInOneFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Multi.java"), `package com.example;

public class Primary {
    public static int value() { return 1; }
}

class Secondary {
    public static int compute() { return 2; }
}
`)
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/User.java"), `package com.example;

public class User {
    public static int use() { return Secondary.compute(); }
}
`)
	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	multi := filepath.Join(dir, "src/main/java/com/example/Multi.java")
	user := filepath.Join(dir, "src/main/java/com/example/User.java")
	if !g.Nodes[user].Imports[multi] {
		t.Errorf("User.java's Imports = %v, want it to include Multi.java via Secondary (its second, package-private top-level type)", g.Nodes[user].Imports)
	}
}

func TestBuildMarksTestFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	cases := []string{"FooTest.java", "TestFoo.java", "FooTests.java", "FooTestCase.java"}
	for _, name := range cases {
		writeFile(t, filepath.Join(dir, "src/test/java/com/example", name), "package com.example;\nclass X {}\n")
	}
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Plain.java"), "package com.example;\nclass Plain {}\n")

	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cases {
		path := filepath.Join(dir, "src/test/java/com/example", name)
		if !g.Nodes[path].HasTestFiles {
			t.Errorf("%s: HasTestFiles = false, want true", name)
		}
	}
	plain := filepath.Join(dir, "src/main/java/com/example/Plain.java")
	if g.Nodes[plain].HasTestFiles {
		t.Error("Plain.java: HasTestFiles = true, want false")
	}
}

func TestBuildSkipsTargetDirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Real.java"), "package com.example;\nclass Real {}\n")
	// A compiled/generated copy under target/ must never be picked up as
	// if it were real source - Maven routinely leaves exactly this behind.
	writeFile(t, filepath.Join(dir, "target/generated-sources/com/example/Real.java"), "package com.example;\nclass Real {}\n")

	g, err := javaanalyzer.New().Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	for id := range g.Nodes {
		if hasPathComponent(id, "target") {
			t.Errorf("node %q was found under target/, want it skipped entirely", id)
		}
	}
}

// TestBuildFailsCleanlyWithoutJava reproduces the one new real precondition
// this analyzer has compared to the old regexp-based version: parsing now
// needs a `java` binary on PATH (to run the embedded FastciJavaParse.java
// shim). Build must report that clearly rather than a bare, confusing
// exec error - the same contract every other fastci subprocess failure
// already honors.
func TestBuildFailsCleanlyWithoutJava(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pom.xml"), "<project></project>")
	writeFile(t, filepath.Join(dir, "src/main/java/com/example/Foo.java"), "package com.example;\nclass Foo {}\n")

	t.Setenv("PATH", "")
	_, err := javaanalyzer.New().Build(dir)
	if err == nil {
		t.Fatal("Build() with no java on PATH = nil error, want one")
	}
	if !strings.Contains(err.Error(), "java not found on PATH") {
		t.Errorf("Build() error = %q, want it to clearly say java isn't on PATH", err.Error())
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
	a := javaanalyzer.New()
	for _, name := range []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradle.properties"} {
		if !a.FullRunFile("/repo/" + name) {
			t.Errorf("FullRunFile(%q) = false, want true", name)
		}
	}
	if a.FullRunFile("/repo/src/main/java/com/example/Foo.java") {
		t.Error("FullRunFile for an ordinary source file = true, want false")
	}
}

func TestIgnorable(t *testing.T) {
	a := javaanalyzer.New()
	if !a.Ignorable("/repo/README.md") {
		t.Error("Ignorable(README.md) = false, want true")
	}
	if a.Ignorable("/repo/src/main/java/com/example/Foo.java") {
		t.Error("Ignorable(Foo.java) = true, want false")
	}
}

// TestIgnorableSkipsGeneratedFilesUnderTargetOrBuild is a regression test
// mirroring csharpanalyzer's own TestIgnorableSkipsSDKGeneratedFilesUnderObj:
// a Maven/Gradle annotation processor (or similar codegen) can emit real
// .java files under target/build, which Build's own skipDirs walk never
// registers as a graph node - without this check they'd look like an
// unresolvable source change and force a full run for no real reason.
func TestIgnorableSkipsGeneratedFilesUnderTargetOrBuild(t *testing.T) {
	a := javaanalyzer.New()
	for _, p := range []string{
		"/repo/target/generated-sources/annotations/com/example/Foo.java",
		"/repo/build/generated/source/proto/main/java/Bar.java",
	} {
		if !a.Ignorable(p) {
			t.Errorf("Ignorable(%q) = false, want true (generated, under target/build)", p)
		}
	}
}

func TestAllTargetsReturnsNil(t *testing.T) {
	a := javaanalyzer.New()
	targets, err := a.AllTargets(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if targets != nil {
		t.Errorf("AllTargets = %v, want nil (mvn/gradle test already discover everything with no selection flag)", targets)
	}
}
