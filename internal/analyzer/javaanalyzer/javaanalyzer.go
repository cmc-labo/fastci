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
// Import resolution is a deliberately simple, regexp-based text scan over
// each .java file's own "package"/"import" declarations and identifier
// tokens - not a real parser, and not javac/jdeps (which would need the
// project already compiled first, unlike every other fastci analyzer).
// This trades some precision for needing nothing beyond the source tree
// itself:
//
//   - Every file is assumed to declare exactly one top-level type whose
//     name matches its filename - true for the overwhelming majority of
//     real Java code, and Java's own style conventions actively encourage
//     it, but a file with more than one top-level type only has its
//     filename-matching type tracked precisely.
//   - A same-package reference needs no import at all in real Java - a
//     test class is routinely written in the very same package as the
//     class it tests for exactly this reason - so a same-package
//     candidate is resolved by checking whether its simple class name
//     appears anywhere in the file's own identifier tokens, rather than
//     either requiring an import that Java doesn't (a real
//     under-approximation risk) or unconditionally linking every file in
//     a package to every other one, which collapses to "the whole
//     package is one unit" the moment a package holds more than a
//     handful of files - exactly the common case for Maven/Gradle's own
//     convention of mirroring src/main/java and src/test/java package
//     layouts. The same token check resolves a wildcard import ("import
//     a.b.*;") against every file in the wildcarded package, for the
//     same reason. Java has no import-renaming syntax, so a real
//     reference to a type always includes its simple name as a literal
//     token somewhere in the file - a token match can occasionally be a
//     false positive (a coincidentally-named comment, string, or local
//     variable), which only ever costs an unnecessary extra test run, not
//     a missed one.
//   - A single-type import ("import a.b.C;", optionally "static") is
//     resolved by exact fully-qualified-name match against every other
//     file's own package+filename; one that doesn't match anything
//     tracked (the overwhelming majority - the JDK itself, third-party
//     libraries) is simply treated as external, the same way a
//     node_modules import is for Jest/Vitest.
package javaanalyzer

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

type javaFile struct {
	path    string
	pkg     string
	class   string // filename without ".java" - see the package doc's scope note.
	imports []importRef
	idents  map[string]bool // every identifier token in the file - see the package doc.
}

type importRef struct {
	fqcn     string
	wildcard bool
}

var (
	packageRE    = regexp.MustCompile(`^\s*package\s+([\w.]+)\s*;`)
	importRE     = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([\w.]+)(\.\*)?\s*;`)
	identifierRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

// parseJavaFile extracts path's package declaration, import list, and
// full identifier-token set via a plain regex scan - see the package doc
// for why this isn't a real parser.
func parseJavaFile(path string) (javaFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return javaFile{}, err
	}
	content := string(data)
	jf := javaFile{
		path:  path,
		class: strings.TrimSuffix(filepath.Base(path), ".java"),
	}
	for _, line := range strings.Split(content, "\n") {
		if m := packageRE.FindStringSubmatch(line); m != nil && jf.pkg == "" {
			jf.pkg = m[1]
			continue
		}
		if m := importRE.FindStringSubmatch(line); m != nil {
			jf.imports = append(jf.imports, importRef{fqcn: m[1], wildcard: m[2] != ""})
		}
	}
	tokens := identifierRE.FindAllString(content, -1)
	jf.idents = make(map[string]bool, len(tokens))
	for _, t := range tokens {
		jf.idents[t] = true
	}
	return jf, nil
}

// fqcnOf returns jf's own fully-qualified class name.
func (jf javaFile) fqcnOf() string {
	if jf.pkg == "" {
		return jf.class
	}
	return jf.pkg + "." + jf.class
}

// Build walks dir for every *.java file, parses each (see parseJavaFile),
// and builds a graph edge for every resolvable reference - see the
// package doc for exactly what "resolvable" means here.
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
	var files []javaFile
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
		if !strings.HasSuffix(d.Name(), ".java") {
			return nil
		}
		jf, err := parseJavaFile(path)
		if err != nil {
			return nil // unreadable file - skip it, don't fail the whole scan.
		}
		files = append(files, jf)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("javaanalyzer: scanning %s: %w", dir, err)
	}

	// fqcnToFile resolves a single-type import to the file that declares
	// it; pkgToFiles is every other file sharing a package (for the
	// same-package token check) and also every file in a wildcard-
	// imported package (the same check, just keyed by a different
	// package name).
	fqcnToFile := make(map[string]string, len(files))
	pkgToFiles := make(map[string][]javaFile, len(files))
	for _, jf := range files {
		fqcnToFile[jf.fqcnOf()] = jf.path
		pkgToFiles[jf.pkg] = append(pkgToFiles[jf.pkg], jf)
	}

	g := graph.New()
	g.DisableDirFallback = true // one node per file - see package doc.

	for _, jf := range files {
		n := g.Node(jf.path)
		n.Files = []string{jf.path}
		if isTestFile(jf.path) {
			n.HasTestFiles = true
		}

		// A same-package reference needs no import in real Java - see
		// the package doc for why this is a token check, not an
		// unconditional edge to every same-package file.
		for _, sibling := range pkgToFiles[jf.pkg] {
			if sibling.path != jf.path && jf.idents[sibling.class] {
				n.Imports[sibling.path] = true
			}
		}

		for _, imp := range jf.imports {
			if imp.wildcard {
				// Same token check as same-package above, just against
				// the wildcarded package's members instead of this
				// file's own - see the package doc.
				for _, member := range pkgToFiles[imp.fqcn] {
					if member.path != jf.path && jf.idents[member.class] {
						n.Imports[member.path] = true
					}
				}
				continue
			}
			fqcn := imp.fqcn
			if target, ok := fqcnToFile[fqcn]; ok {
				if target != jf.path {
					n.Imports[target] = true
				}
				continue
			}
			// A "static" import names a member, not a class, as its last
			// segment (e.g. "import static a.b.C.FOO;") - if the whole
			// path isn't a known class, try again without that last
			// segment before concluding it's external (JDK/third-party).
			if i := strings.LastIndex(fqcn, "."); i > 0 {
				if target, ok := fqcnToFile[fqcn[:i]]; ok && target != jf.path {
					n.Imports[target] = true
				}
			}
			// Otherwise: external (JDK, a third-party library, or a type
			// genuinely not part of this project) - not a graph edge,
			// the same treatment a node_modules import gets for Jest.
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

// Ignorable reports whether a changed non-Java file is safe to ignore.
func (*Analyzer) Ignorable(absPath string) bool {
	return filepath.Ext(absPath) != ".java"
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
