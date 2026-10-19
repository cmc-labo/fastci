// Package javaanalyzer implements the fastci analyzer.Analyzer interface
// for Java projects built with Maven (pom.xml) or Gradle
// (build.gradle/build.gradle.kts), at file granularity (like Jest/Vitest/
// pytest). Multi-module Maven builds (a parent pom.xml's <modules>) and
// multi-project Gradle builds (settings.gradle's include(...)) are both
// supported: Build walks the whole project tree regardless of module
// boundaries, so a cross-module import (one module's class imported by
// another, resolved via Maven/Gradle coordinates rather than a relative
// path) is tracked by the exact same fully-qualified-name matching as any
// other import - nothing module-aware needed there at all, since from a
// plain source-text point of view a package is a package regardless of
// which module's src/ tree it happens to live under.
//
// RunTests needs two real fixes to make a multi-module selection actually
// run correctly, both verified directly against real multi-module
// Maven/Gradle builds while adding this support - see RunTests' own doc
// comment for the mechanics: Maven Surefire's and Gradle's own default
// behavior is to fail the whole build the moment a test-class filter
// matches nothing in some module/subproject, which is exactly what
// happens, by design, in every module that isn't part of a given
// selection - the entire point of impact analysis. Both are told not to.
// RunTests also selects by fully-qualified class name rather than bare
// simple name for this same reason: two different modules can easily
// share a simple class name, and Gradle's own --tests matching isn't
// reliably simple-name-aware without one.
//
// Import resolution delegates to the real language tooling, the same
// "don't reimplement what the toolchain already does" approach as
// go/packages for Go or cargo metadata for Rust: FastciJavaParse.java, a
// small shim embedded in this package, uses the JDK's own Compiler Tree
// API (com.sun.source.*, javac's own frontend parser) in parse-only mode
// (JavacTask.parse(), never .analyze()) - the same real-tool delegation
// pytestanalyzer already uses its own embedded resolve_imports.py for.
// "Parse-only" is the key property: unlike jdeps (which can only analyze
// already-compiled .class/JAR files), this needs nothing beyond the
// source tree itself - no classpath, no dependency resolution, no
// successful compile - preserving the same "never requires the project
// already built" guarantee every other fastci analyzer maintains. It
// does need a `java` binary on PATH, which in practice is never a new
// requirement: anything that can run `mvn test`/`gradle test` at all
// already has a JDK installed, since Maven and Gradle are themselves Java
// programs.
//
// A real parse eliminates two genuine precision bugs a text-based regex
// scan had (verified directly, by testing the parser shim standalone
// before wiring it into Go at all): a commented-out or string-literal
// "import ...;"/"package ...;" is correctly never seen as a real
// declaration (a regex scanning line-by-line has no notion of a block
// comment or a string literal), and a file declaring more than one
// top-level type is handled correctly (a regex assumed exactly one,
// matching the filename). What a parse-only pass still can't do, by
// design, no matter how good the parser is: fully resolve a same-package
// reference (needs no import at all in real Java - a test class is
// routinely written in the very same package as the class it tests, for
// exactly this reason) or a wildcard import's actual members, since both
// require complete semantic resolution against a real classpath - the
// exact thing parse-only mode deliberately avoids needing. Both are
// instead resolved by checking whether the referenced type's simple name
// appears among the real identifier tokens the parser found in the
// file's actual code - not its comments or string literals, unlike the
// old regex version, but still a token check rather than true symbol
// resolution, so an occasional false positive (a coincidentally-named
// local variable or field) is possible - always safe-direction
// over-inclusion (an unnecessary extra test run), never a missed one,
// since Java has no import-renaming syntax: a real reference to a type
// always includes its simple name as a literal token somewhere in the
// file's real code.
package javaanalyzer

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cmc-labo/fastci/internal/debug"
	"github.com/cmc-labo/fastci/internal/graph"
	"github.com/cmc-labo/fastci/internal/runner"
)

// Analyzer is the Maven/Gradle Java implementation of analyzer.Analyzer.
type Analyzer struct{}

// New returns a Java analyzer.
func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) Name() string { return "java" }

// Detect looks for a Maven (pom.xml) or Gradle (build.gradle/
// build.gradle.kts) project root - for a multi-module build, that's the
// aggregator/parent root (the one listing <modules>/include(...)), the
// same place every other analyzer's own "run from the project root"
// convention already expects.
func (*Analyzer) Detect(dir string) (bool, error) {
	_, ok := buildTool(dir)
	return ok, nil
}

// buildTool reports which build tool's manifest is present at dir, and
// the exact binary name fastci would need on PATH as a fallback when
// there's no wrapper script (see mavenArgv/gradleArgv).
func buildTool(dir string) (tool string, ok bool) {
	if fileExists(filepath.Join(dir, "pom.xml")) {
		return "mvn", true
	}
	if fileExists(filepath.Join(dir, "build.gradle")) || fileExists(filepath.Join(dir, "build.gradle.kts")) {
		return "gradle", true
	}
	return "", false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// skipDirs are directories never worth walking into: build output
// (target/build), VCS/tooling metadata, and dependency trees that
// couldn't contain this project's own source anyway.
var skipDirs = map[string]bool{
	"target": true, "build": true, ".git": true, ".gradle": true,
	".mvn": true, "node_modules": true,
}

//go:embed FastciJavaParse.java
var parserSource []byte

// parsedFile is one file's entry in FastciJavaParse.java's own JSON
// output - see that file's doc comment for exactly what each field means
// and how it was derived.
type parsedFile struct {
	Package         string   `json:"package"`
	Types           []string `json:"types"`
	Imports         []string `json:"imports"`
	WildcardImports []string `json:"wildcardImports"`
	Identifiers     []string `json:"identifiers"`
}

type parserOutput struct {
	Files map[string]parsedFile `json:"files"`
}

// writeParserScript writes the embedded Java shim to a file named to
// match its own public class name, FastciJavaParse.java - required for
// `java <file>.java` single-file source-code launch (JEP 330, Java 11+),
// which has the same "filename must match the public class" restriction
// plain javac does. Written into a fresh temp directory (rather than
// alongside any real source file) so it can never collide with one.
func writeParserScript() (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "fastci-java-parse-*")
	if err != nil {
		return "", nil, fmt.Errorf("javaanalyzer: %w", err)
	}
	path = filepath.Join(dir, "FastciJavaParse.java")
	if err := os.WriteFile(path, parserSource, 0o644); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("javaanalyzer: %w", err)
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

// parseJavaFiles runs FastciJavaParse.java (see the package doc) against
// every path, feeding the list via stdin (one per line) rather than argv
// to avoid any risk of an argv-length limit on a large project, and
// returns the parsed result keyed by absolute file path.
func parseJavaFiles(paths []string) (map[string]parsedFile, error) {
	if len(paths) == 0 {
		return map[string]parsedFile{}, nil
	}
	javaBin, err := exec.LookPath("java")
	if err != nil {
		return nil, fmt.Errorf("javaanalyzer: java not found on PATH (needed to parse Java source via the JDK's own compiler frontend - install a JDK, e.g. from https://adoptium.net; anything that can run mvn/gradle already has one, since both are themselves Java programs)")
	}

	scriptPath, cleanup, err := writeParserScript()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	cmd := exec.Command(javaBin, scriptPath)
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\n"))
	done := debug.Command("javaanalyzer", cmd.Args, "")
	out, err := cmd.Output()
	done()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
				return nil, fmt.Errorf("javaanalyzer: parsing Java source: %w\n%s", err, stderr)
			}
		}
		return nil, fmt.Errorf("javaanalyzer: parsing Java source: %w", err)
	}

	var parsed parserOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("javaanalyzer: parsing FastciJavaParse.java's output: %w", err)
	}
	return parsed.Files, nil
}

// Build walks dir for every *.java file, parses all of them in one shim
// invocation (see parseJavaFiles), and builds a graph edge for every
// resolvable reference - see the package doc for exactly what
// "resolvable" means here.
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
	var paths []string
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
		if strings.HasSuffix(d.Name(), ".java") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("javaanalyzer: scanning %s: %w", dir, err)
	}

	files, err := parseJavaFiles(paths)
	if err != nil {
		return nil, err
	}

	// fqcnToFile resolves a direct type reference (a plain single-type
	// import, or a static member/wildcard import - FastciJavaParse.java
	// has already reduced both of those to the one type they actually
	// name) to the file that declares it. Every top-level type a file
	// declares is registered, not just one per file - a real parse
	// handles more than one top-level type per file correctly, unlike
	// the old filename-matching assumption. pkgToFiles is every file in
	// a given package, for the same-package and wildcard-import token
	// checks (see the package doc).
	fqcnToFile := make(map[string]string, len(files))
	pkgToFiles := make(map[string][]string, len(files))
	for path, pf := range files {
		pkgToFiles[pf.Package] = append(pkgToFiles[pf.Package], path)
		for _, t := range pf.Types {
			fqcn := t
			if pf.Package != "" {
				fqcn = pf.Package + "." + t
			}
			fqcnToFile[fqcn] = path
		}
	}

	// hasAnyType reports whether path declares a type whose simple name
	// is among idents - the shared token check both the same-package and
	// wildcard-import resolution below use.
	hasAnyType := func(path string, idents map[string]bool) bool {
		for _, t := range files[path].Types {
			if idents[t] {
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
		if isTestFile(path) {
			n.HasTestFiles = true
		}

		idents := make(map[string]bool, len(pf.Identifiers))
		for _, id := range pf.Identifiers {
			idents[id] = true
		}

		// A same-package reference needs no import in real Java - see
		// the package doc for why this is a token check, not an
		// unconditional edge to every same-package file.
		for _, sibling := range pkgToFiles[pf.Package] {
			if sibling != path && hasAnyType(sibling, idents) {
				n.Imports[sibling] = true
			}
		}

		for _, fqcn := range pf.Imports {
			if target, ok := fqcnToFile[fqcn]; ok && target != path {
				n.Imports[target] = true
			}
			// Otherwise: external (JDK, a third-party library, or a type
			// genuinely not part of this project) - not a graph edge,
			// the same treatment a node_modules import gets for Jest.
		}

		for _, pkg := range pf.WildcardImports {
			// Same token check as same-package above, just against the
			// wildcarded package's members instead of this file's own -
			// see the package doc.
			for _, member := range pkgToFiles[pkg] {
				if member != path && hasAnyType(member, idents) {
					n.Imports[member] = true
				}
			}
		}
	}

	g.IndexFiles()
	g.BuildImporters()
	return g, nil
}

// testFileRE matches the test-file conventions Maven Surefire
// (https://maven.apache.org/surefire/maven-surefire-plugin/test-mojo.html#includes)
// and Gradle's default JUnit discovery both document: Test*.java,
// *Test.java, *Tests.java, *TestCase.java.
var testFileRE = regexp.MustCompile(`^(Test.+|.+Test|.+Tests|.+TestCase)\.java$`)

func isTestFile(absPath string) bool {
	return testFileRE.MatchString(filepath.Base(absPath))
}

// fullRunBasenames are build-tool manifests whose effect on the build
// (dependencies, plugins, source sets, Java version) isn't captured by
// the import graph at all.
var fullRunBasenames = map[string]bool{
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true,
	"settings.gradle": true, "settings.gradle.kts": true, "gradle.properties": true,
}

// FullRunFile reports whether a changed file should force a full test
// run: any build-tool manifest qualifies - see fullRunBasenames.
func (*Analyzer) FullRunFile(absPath string) bool {
	return fullRunBasenames[filepath.Base(absPath)]
}

// Ignorable reports whether a changed non-.java file, or a file under
// target/build (skipDirs), is safe to ignore. The skipDirs check matters
// for the same reason csharpanalyzer's own equivalent does: a Maven/
// Gradle annotation processor (or protobuf, or similar) can generate real
// .java files under target/generated-sources - without this check, a
// project whose target/ isn't gitignored would see those as unresolvable
// source changes (Build's own skipDirs walk never registers them as
// graph nodes) and force a full run for no real reason.
func (*Analyzer) Ignorable(absPath string) bool {
	if filepath.Ext(absPath) != ".java" {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(absPath), "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}

// AllTargets returns nil: both `mvn test` and `gradle test`, run with no
// test-selection flag at all, already discover and run everything - a
// full/--all run needs no explicit target list, the same as pytest.
func (*Analyzer) AllTargets(dir string) ([]string, error) {
	return nil, nil
}

// RunTests runs the project's own build tool - preferring a committed
// wrapper script (mvnw/gradlew) over a global install, the same
// reproducible-build convention real Maven/Gradle projects already widely
// use - selecting targets (absolute .java file paths) by fully-qualified
// class name (see fqcnFromPath) and, when any selection is made at all,
// telling the build tool not to fail just because some other module in
// the build has no matching test class (see the doc on
// gradleInitScriptDisablingFailOnNoMatch for why this specifically
// matters for a multi-module build). An empty targets (a full/--all run)
// omits the selection flag entirely, letting the build tool's own default
// (discover and run everything) apply.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	tool, ok := buildTool(dir)
	if !ok {
		return fmt.Errorf("javaanalyzer: no pom.xml or build.gradle(.kts) found in %s", dir)
	}

	fqcns := make([]string, len(targets))
	for i, t := range targets {
		fqcns[i] = fqcnFromPath(t)
	}

	var argv []string
	var cleanup func()
	if tool == "mvn" {
		argv = mavenArgv(dir)
		if len(fqcns) > 0 {
			// -DfailIfNoTests=false: see the RunTests doc comment and
			// gradleInitScriptDisablingFailOnNoMatch's equivalent below -
			// Surefire's own default is to fail the whole module (and, in
			// a multi-module reactor, everything downstream of it) the
			// moment -Dtest matches zero classes in it, which is exactly
			// what happens in every module that isn't part of this
			// particular selection.
			argv = append(argv, "-Dtest="+strings.Join(fqcns, ","), "-DfailIfNoTests=false")
		}
	} else {
		argv = gradleArgv(dir)
		if len(fqcns) > 0 {
			scriptPath, c, err := writeGradleInitScript()
			if err != nil {
				return err
			}
			cleanup = c
			argv = append(argv, "--init-script", scriptPath)
			for _, fqcn := range fqcns {
				argv = append(argv, "--tests", fqcn)
			}
		}
	}
	if cleanup != nil {
		defer cleanup()
	}
	argv = append(argv, extraArgs...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
}

// fqcnFromPath derives absPath's fully-qualified class name from the
// standard Maven/Gradle source-root convention
// (src/main/java/<pkg/path>/Class.java or src/test/java/<pkg/path>/
// Class.java), falling back to the bare simple class name if absPath
// isn't under either. This is more than cosmetic: in a multi-module
// project, two different modules' packages can easily share a simple
// class name (e.g. two modules each with their own "package-info"-style
// "Config" class), and the simple name alone would ambiguously select
// both; a fully-qualified name is exact. It also happens to be the only
// form of --tests pattern Gradle reliably matches by itself (a bare
// simple class name with no wildcard isn't guaranteed to match at all,
// verified directly against a real multi-project build) - Maven's own
// -Dtest accepts either form equally.
func fqcnFromPath(absPath string) string {
	norm := filepath.ToSlash(absPath)
	for _, root := range []string{"src/main/java/", "src/test/java/"} {
		if i := strings.LastIndex(norm, root); i >= 0 {
			rel := strings.TrimSuffix(norm[i+len(root):], ".java")
			return strings.ReplaceAll(rel, "/", ".")
		}
	}
	return strings.TrimSuffix(filepath.Base(absPath), ".java")
}

// gradleInitScriptDisablingFailOnNoMatch is injected via --init-script
// (so it applies without editing the project's own build.gradle) and
// disables every Test task's failOnNoMatchingTests - Gradle's own
// default behavior is to fail the *entire build* the moment a --tests
// filter matches zero classes in some project's test task, which is
// exactly what happens, by design, in every module that isn't part of
// the current selection in a multi-project build (verified directly
// against a real multi-project Gradle build: a plain `gradle test
// --tests Foo` reliably fails with "No tests found for given includes"
// the moment any other subproject's own test task has nothing matching
// Foo, even though Foo itself would otherwise pass cleanly).
const gradleInitScriptDisablingFailOnNoMatch = `allprojects {
    tasks.withType(Test) {
        filter {
            setFailOnNoMatchingTests(false)
        }
    }
}
`

func writeGradleInitScript() (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "fastci-gradle-init-*.gradle")
	if err != nil {
		return "", nil, fmt.Errorf("javaanalyzer: %w", err)
	}
	if _, err := f.WriteString(gradleInitScriptDisablingFailOnNoMatch); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("javaanalyzer: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("javaanalyzer: %w", err)
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func mavenArgv(dir string) []string {
	if wrapper := filepath.Join(dir, "mvnw"); fileExists(wrapper) {
		return []string{wrapper, "test"}
	}
	if path, err := exec.LookPath("mvn"); err == nil {
		return []string{path, "test"}
	}
	return []string{"mvn", "test"}
}

func gradleArgv(dir string) []string {
	if wrapper := filepath.Join(dir, "gradlew"); fileExists(wrapper) {
		return []string{wrapper, "test"}
	}
	if path, err := exec.LookPath("gradle"); err == nil {
		return []string{path, "test"}
	}
	return []string{"gradle", "test"}
}
