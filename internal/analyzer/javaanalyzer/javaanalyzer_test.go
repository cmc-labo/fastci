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
