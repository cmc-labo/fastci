package javaanalyzer_test

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/cmc-labo/fastci/internal/analyzer/javaanalyzer"
	"github.com/cmc-labo/fastci/internal/impact"
)

// This file is a comprehensive integration test suite for the Java
// multi-module support added on top of the original single-module
// analyzer: it verifies, against real multi-module project layouts of a
// few different shapes (a linear chain, a diamond, and always at least
// one module genuinely unrelated to anything that changed), both that
// impact analysis selects exactly the right test targets (fast, no
// mvn/gradle needed - see TestMultiModuleSelection) and that actually
// running the selected targets against the real build tool succeeds and
// runs exactly those targets (slower, real integration tests, skipped if
// mvn/gradle aren't installed - see TestMultiModuleMavenRealExecution*/
// TestMultiModuleGradleRealExecution*). The latter exist specifically
// because two real bugs were found by exactly this kind of real-tool
// testing while adding multi-module support in the first place, neither
// of which a selection-only test (or a single-module test) could ever
// catch: Maven Surefire and Gradle both fail the *entire* build the
// moment a test-class filter matches nothing in some module that wasn't
// part of the current selection, and Gradle's own --tests matching isn't
// reliable against a bare simple class name - see javaanalyzer.go's own
// package doc for the fixes (-DfailIfNoTests=false, a generated
// --init-script, selecting by fully-qualified name).

// moduleSpec describes one module in a synthetic multi-module project: its
// name and which other modules' classes its own main class references.
// Every module gets a real cross-module import for each dep (resolved via
// Maven/Gradle coordinates, not a relative path) and a real test class
// that calls its own main class - so a selected/unselected test target is
// always a genuine, compilable, runnable JUnit test, not a stub.
type moduleSpec struct {
	name string
	deps []string
}

func className(moduleName string) string {
	return strings.ToUpper(moduleName[:1]) + moduleName[1:] + "Lib"
}

// writeJavaModuleSource writes spec's main class and test class under
// moduleDir, following the standard src/main/java + src/test/java layout.
func writeJavaModuleSource(t *testing.T, moduleDir string, spec moduleSpec) {
	t.Helper()
	pkg := "com.example." + spec.name
	class := className(spec.name)

	var imports, calls strings.Builder
	for _, d := range spec.deps {
		depClass := className(d)
		fmt.Fprintf(&imports, "import com.example.%s.%s;\n", d, depClass)
		fmt.Fprintf(&calls, "        sb.append(%s.describe());\n", depClass)
	}

	main := fmt.Sprintf(`package %s;

%s
public class %s {
    public static String describe() {
        StringBuilder sb = new StringBuilder("%s");
%s        return sb.toString();
    }
}
`, pkg, imports.String(), class, spec.name, calls.String())
	writeJavaFile(t, filepath.Join(moduleDir, "src", "main", "java"), pkg, class, main)

	test := fmt.Sprintf(`package %s;

import org.junit.Test;
import static org.junit.Assert.assertNotNull;

public class %sTest {
    @Test
    public void test%s() {
        assertNotNull(%s.describe());
    }
}
`, pkg, class, class, class)
	writeJavaFile(t, filepath.Join(moduleDir, "src", "test", "java"), pkg, class+"Test", test)
}

func writeJavaFile(t *testing.T, srcRoot, pkg, class, content string) {
	t.Helper()
	path := filepath.Join(srcRoot, filepath.Join(strings.Split(pkg, ".")...), class+".java")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mainClassPath returns the absolute path to moduleName's own main class,
// the file a test touches to simulate "this module changed".
func mainClassPath(root, moduleName string) string {
	return filepath.Join(root, moduleName, "src", "main", "java", "com", "example", moduleName, className(moduleName)+".java")
}

// linearChainSpecs is leaf <- mid <- consumer (consumer depends on mid,
// mid depends on leaf), plus a module genuinely unrelated to any of them.
func linearChainSpecs() []moduleSpec {
	return []moduleSpec{
		{name: "leaf"},
		{name: "mid", deps: []string{"leaf"}},
		{name: "consumer", deps: []string{"mid"}},
		{name: "unrelated"},
	}
}

// diamondSpecs is leaf <- {left, right} <- top (top depends on both left
// and right, each of which independently depends on leaf), plus a module
// genuinely unrelated to any of them.
func diamondSpecs() []moduleSpec {
	return []moduleSpec{
		{name: "leaf"},
		{name: "left", deps: []string{"leaf"}},
		{name: "right", deps: []string{"leaf"}},
		{name: "top", deps: []string{"left", "right"}},
		{name: "unrelated"},
	}
}

// writeMavenMultiModule writes a parent aggregator pom.xml plus one child
// pom.xml per spec, with a real <dependency> entry (Maven coordinates, not
// a relative path) for every declared module dependency.
func writeMavenMultiModule(t *testing.T, root string, specs []moduleSpec) {
	t.Helper()
	var modules strings.Builder
	for _, s := range specs {
		fmt.Fprintf(&modules, "    <module>%s</module>\n", s.name)
	}
	writeFile(t, filepath.Join(root, "pom.xml"), fmt.Sprintf(`<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>com.example</groupId>
  <artifactId>parent</artifactId>
  <version>1.0.0</version>
  <packaging>pom</packaging>
  <modules>
%s  </modules>
</project>
`, modules.String()))

	for _, s := range specs {
		var deps strings.Builder
		for _, d := range s.deps {
			fmt.Fprintf(&deps, "    <dependency><groupId>com.example</groupId><artifactId>%s</artifactId><version>1.0.0</version></dependency>\n", d)
		}
		fmt.Fprint(&deps, "    <dependency><groupId>junit</groupId><artifactId>junit</artifactId><version>4.13.2</version><scope>test</scope></dependency>\n")
		writeFile(t, filepath.Join(root, s.name, "pom.xml"), fmt.Sprintf(`<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <parent><groupId>com.example</groupId><artifactId>parent</artifactId><version>1.0.0</version></parent>
  <artifactId>%s</artifactId>
  <properties>
    <maven.compiler.source>11</maven.compiler.source>
    <maven.compiler.target>11</maven.compiler.target>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
  <dependencies>
%s  </dependencies>
</project>
`, s.name, deps.String()))
		writeJavaModuleSource(t, filepath.Join(root, s.name), s)
	}
}

// writeGradleMultiModule writes settings.gradle, a root build.gradle
// shared by every subproject, and one child build.gradle per spec with a
// real `project(':dep')` dependency for every declared module dependency.
func writeGradleMultiModule(t *testing.T, root string, specs []moduleSpec) {
	t.Helper()
	var includes []string
	for _, s := range specs {
		includes = append(includes, "'"+s.name+"'")
	}
	writeFile(t, filepath.Join(root, "settings.gradle"), "include "+strings.Join(includes, ", ")+"\n")
	writeFile(t, filepath.Join(root, "build.gradle"), `subprojects {
    apply plugin: 'java'
    repositories { mavenCentral() }
    dependencies {
        testCompile 'junit:junit:4.13.2'
    }
}
`)

	for _, s := range specs {
		var deps strings.Builder
		for _, d := range s.deps {
			fmt.Fprintf(&deps, "    compile project(':%s')\n", d)
		}
		content := ""
		if deps.Len() > 0 {
			content = "dependencies {\n" + deps.String() + "}\n"
		}
		writeFile(t, filepath.Join(root, s.name, "build.gradle"), content)
		writeJavaModuleSource(t, filepath.Join(root, s.name), s)
	}
}

// moduleOfTarget returns which spec's module a selected target (an
// absolute .java file path) belongs to, by checking which module
// directory it's under - simpler and just as reliable as parsing the
// package out of the path, since every module's directory name is also
// its own unique path component here.
func moduleOfTarget(root, target string, specs []moduleSpec) (string, bool) {
	for _, s := range specs {
		if strings.HasPrefix(target, filepath.Join(root, s.name)+string(filepath.Separator)) {
			return s.name, true
		}
	}
	return "", false
}

func selectedModuleNames(t *testing.T, root string, specs []moduleSpec, changedModule string) []string {
	t.Helper()
	a := javaanalyzer.New()
	g, err := a.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res := impact.Compute(g, []string{mainClassPath(root, changedModule)}, a)
	if res.FullRun {
		t.Fatalf("unexpected full run; reasons: %v", res.FullRunReasons)
	}
	seen := map[string]bool{}
	for _, target := range res.Targets {
		m, ok := moduleOfTarget(root, target, specs)
		if !ok {
			t.Fatalf("selected target %q doesn't belong to any known module", target)
		}
		seen[m] = true
	}
	var names []string
	for m := range seen {
		names = append(names, m)
	}
	sort.Strings(names)
	return names
}

// TestMultiModuleSelection is the fast half of this suite: it never
// shells out to mvn/gradle at all (javaanalyzer.Build is pure
// filesystem/regexp analysis), so it always runs, verifying selection
// precision and recall across a linear chain and a diamond - both
// transitively propagating a change through the right modules and never
// selecting the always-present unrelated module.
func TestMultiModuleSelection(t *testing.T) {
	tests := []struct {
		name          string
		specs         []moduleSpec
		changedModule string
		want          []string
	}{
		{"linear chain, change the leaf", linearChainSpecs(), "leaf", []string{"consumer", "leaf", "mid"}},
		{"linear chain, change the middle", linearChainSpecs(), "mid", []string{"consumer", "mid"}},
		{"linear chain, change the consumer itself", linearChainSpecs(), "consumer", []string{"consumer"}},
		{"diamond, change the shared leaf", diamondSpecs(), "leaf", []string{"leaf", "left", "right", "top"}},
		{"diamond, change only the left branch", diamondSpecs(), "left", []string{"left", "top"}},
		{"diamond, change only the right branch", diamondSpecs(), "right", []string{"right", "top"}},
		{"diamond, change the top itself", diamondSpecs(), "top", []string{"top"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			// Maven vs Gradle manifest makes no difference to Build's own
			// resolution - it never calls Detect - so either works here;
			// Maven is picked arbitrarily for this fast half of the suite.
			writeMavenMultiModule(t, root, tt.specs)
			got := selectedModuleNames(t, root, tt.specs, tt.changedModule)
			if !equalStrings(got, tt.want) {
				t.Errorf("selected modules = %v, want %v", got, tt.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mavenTestReportCount returns how many JUnit test methods Surefire
// actually recorded as run for moduleName, by parsing its own
// target/surefire-reports/TEST-*.xml - the authoritative record of what
// really executed, independent of (and a check on) whatever fastci itself
// claims it selected.
func mavenTestReportCount(root, moduleName string) int {
	matches, _ := filepath.Glob(filepath.Join(root, moduleName, "target", "surefire-reports", "TEST-*.xml"))
	total := 0
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var suite struct {
			Tests int `xml:"tests,attr"`
		}
		if xml.Unmarshal(data, &suite) == nil {
			total += suite.Tests
		}
	}
	return total
}

var gradleTestCounterRE = regexp.MustCompile(`<div class="counter">(\d+)</div>\s*<p>tests</p>`)

// gradleTestReportCount returns how many tests Gradle's own HTML report
// recorded as run for moduleName - the authoritative record of what
// really executed, the same role mavenTestReportCount plays for Maven.
func gradleTestReportCount(root, moduleName string) int {
	data, err := os.ReadFile(filepath.Join(root, moduleName, "build", "reports", "tests", "test", "index.html"))
	if err != nil {
		return 0
	}
	m := gradleTestCounterRE.FindSubmatch(data)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

func gitInitCommit(t *testing.T, dir string) {
	t.Helper()
	runGitCmd(t, dir, "init", "-q")
	runGitCmd(t, dir, "add", "-A")
	runGitCmd(t, dir, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", "init")
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestMultiModuleMavenRealExecution is the real-tool half of this suite
// for Maven: it uses the diamond layout, changes only the "left" branch
// (so only left+top are selected - leaf, right, and unrelated are not),
// and actually runs RunTests against the real selection, verifying both
// that the build succeeds (the real regression: Surefire's own default
// is to fail the entire build the moment -Dtest matches nothing in some
// *other* module, e.g. leaf/right/unrelated here) and that Surefire's own
// reports confirm exactly the right test classes actually ran - not just
// that fastci claims they would.
func TestMultiModuleMavenRealExecution(t *testing.T) {
	if _, err := exec.LookPath("mvn"); err != nil {
		t.Skip("mvn not installed - skipping this real integration test")
	}

	root := t.TempDir()
	specs := diamondSpecs()
	writeMavenMultiModule(t, root, specs)
	gitInitCommit(t, root)

	a := javaanalyzer.New()
	g, err := a.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res := impact.Compute(g, []string{mainClassPath(root, "left")}, a)
	if res.FullRun {
		t.Fatalf("unexpected full run; reasons: %v", res.FullRunReasons)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120_000_000_000) // 120s
	defer cancel()
	if err := a.RunTests(ctx, root, res.Targets, nil); err != nil {
		t.Fatalf("RunTests: %v (want the build to succeed even though leaf/right/unrelated have nothing selected)", err)
	}

	wantCounts := map[string]int{"leaf": 0, "left": 1, "right": 0, "top": 1, "unrelated": 0}
	for module, want := range wantCounts {
		if got := mavenTestReportCount(root, module); got != want {
			t.Errorf("module %q: Surefire ran %d test(s), want %d", module, got, want)
		}
	}
}

// TestMultiModuleGradleRealExecution mirrors
// TestMultiModuleMavenRealExecution for Gradle - the real regression
// there is Gradle's own default failOnNoMatchingTests, and separately,
// that Gradle's --tests matching a bare simple class name isn't reliable
// (verified directly against a real build while fixing this), which is
// why targets are selected by fully-qualified name.
func TestMultiModuleGradleRealExecution(t *testing.T) {
	if _, err := exec.LookPath("gradle"); err != nil {
		t.Skip("gradle not installed - skipping this real integration test")
	}

	root := t.TempDir()
	specs := diamondSpecs()
	writeGradleMultiModule(t, root, specs)
	gitInitCommit(t, root)

	a := javaanalyzer.New()
	g, err := a.Build(root)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	res := impact.Compute(g, []string{mainClassPath(root, "left")}, a)
	if res.FullRun {
		t.Fatalf("unexpected full run; reasons: %v", res.FullRunReasons)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180_000_000_000) // 180s - a cold Gradle daemon start is slow.
	defer cancel()
	if err := a.RunTests(ctx, root, res.Targets, nil); err != nil {
		t.Fatalf("RunTests: %v (want the build to succeed even though leaf/right/unrelated have nothing selected)", err)
	}

	wantCounts := map[string]int{"leaf": 0, "left": 1, "right": 0, "top": 1, "unrelated": 0}
	for module, want := range wantCounts {
		if got := gradleTestReportCount(root, module); got != want {
			t.Errorf("module %q: Gradle ran %d test(s), want %d", module, got, want)
		}
	}
}
