// Package javaanalyzer implements the fastci analyzer.Analyzer interface
// for Java projects built with Maven (pom.xml) or Gradle
// (build.gradle/build.gradle.kts), at file granularity (like Jest/Vitest/
// pytest).
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
// build.gradle.kts) project root. Multi-module builds aren't specifically
// handled (see the package doc and README) - only the root manifest is
// checked, the same single-project scope every other check in this file
// assumes.
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
// use - selecting targets (absolute .java file paths) by simple class
// name. An empty targets (a full/--all run) omits the selection flag
// entirely, letting the build tool's own default (discover and run
// everything) apply.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	tool, ok := buildTool(dir)
	if !ok {
		return fmt.Errorf("javaanalyzer: no pom.xml or build.gradle(.kts) found in %s", dir)
	}

	classNames := make([]string, len(targets))
	for i, t := range targets {
		classNames[i] = strings.TrimSuffix(filepath.Base(t), ".java")
	}

	var argv []string
	if tool == "mvn" {
		argv = mavenArgv(dir)
		if len(classNames) > 0 {
			argv = append(argv, "-Dtest="+strings.Join(classNames, ","))
		}
	} else {
		argv = gradleArgv(dir)
		for _, c := range classNames {
			argv = append(argv, "--tests", c)
		}
	}
	argv = append(argv, extraArgs...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
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
