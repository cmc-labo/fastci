package impact_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer"
	"github.com/hpscript/fastci/internal/analyzer/cargoanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/goanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/jestanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/pytestanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/vitestanalyzer"
	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/impact"
)

var goAnalyzer analyzer.Analyzer = goanalyzer.New()
var jestAnalyzer analyzer.Analyzer = jestanalyzer.New()
var pytestAnalyzer analyzer.Analyzer = pytestanalyzer.New()
var cargoAnalyzer analyzer.Analyzer = cargoanalyzer.New()
var vitestAnalyzer analyzer.Analyzer = vitestanalyzer.New()

func loadSampleGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplemod"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := goAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputeTransitiveImpact(t *testing.T) {
	g, dir := loadSampleGraph(t)

	// pkgc is a leaf dependency of pkgb, which is a dependency of pkga.
	// Changing it should select all three.
	res := impact.Compute(g, []string{filepath.Join(dir, "pkgc", "pkgc.go")}, goAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{"samplemod/pkga", "samplemod/pkgb", "samplemod/pkgc"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeLeafChangeIsIsolated(t *testing.T) {
	g, dir := loadSampleGraph(t)

	// Nothing imports pkga, so changing it should only select itself.
	res := impact.Compute(g, []string{filepath.Join(dir, "pkga", "pkga.go")}, goAnalyzer)
	want := []string{"samplemod/pkga"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeTestOnlyDependencyEdge(t *testing.T) {
	g, dir := loadSampleGraph(t)

	// pkge depends on pkgd only from its _test.go file; that edge must
	// still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "pkgd", "pkgd.go")}, goAnalyzer)
	want := []string{"samplemod/pkgd", "samplemod/pkge"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeGoModTriggersFullRun(t *testing.T) {
	g, dir := loadSampleGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "go.mod")}, goAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run when go.mod changes")
	}
	want := []string{"samplemod/pkga", "samplemod/pkgb", "samplemod/pkgc", "samplemod/pkgd", "samplemod/pkge"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeNonGoFileIsIgnored(t *testing.T) {
	g, dir := loadSampleGraph(t)

	res := impact.Compute(g, []string{
		filepath.Join(dir, "pkgc", "pkgc.go"),
		filepath.Join(dir, "README.md"),
	}, goAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{"samplemod/pkga", "samplemod/pkgb", "samplemod/pkgc"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeUnresolvedGoFileTriggersFullRun(t *testing.T) {
	g, dir := loadSampleGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "pkgf", "ghost.go")}, goAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run for an unresolvable .go file")
	}
}

func loadSampleWorkspaceGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "sampleworkspace"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := goAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputeCrossModuleWorkspaceImpact(t *testing.T) {
	g, dir := loadSampleWorkspaceGraph(t)

	// pkgleaf lives in module wsleaf; pkgconsumer lives in module wsroot
	// and imports it via the workspace (no replace directive). Changing
	// pkgleaf must still be attributed across the module boundary.
	res := impact.Compute(g, []string{filepath.Join(dir, "modleaf", "pkgleaf", "pkgleaf.go")}, goAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{"wsleaf/pkgleaf", "wsroot/pkgconsumer"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeGoWorkTriggersFullRun(t *testing.T) {
	g, dir := loadSampleWorkspaceGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "go.work")}, goAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run when go.work changes")
	}
}

func loadSampleJestGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplejest"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := jestAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputeJestTransitiveImpactThroughTsconfigAlias(t *testing.T) {
	g, dir := loadSampleJestGraph(t)

	// leaf.ts <- mid.ts (relative import) <- consumer.ts (tsconfig path
	// alias @app/mid) <- consumer.test.ts. leaf.test.ts also imports
	// leaf.ts directly. isolated.test.ts must NOT be selected.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "leaf.ts")}, jestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{
		filepath.Join(dir, "src", "consumer.test.ts"),
		filepath.Join(dir, "src", "leaf.test.ts"),
		filepath.Join(dir, "src", "mid.test.ts"),
	}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeJestTestOnlyDependencyEdge(t *testing.T) {
	g, dir := loadSampleJestGraph(t)

	// testutil.ts is imported only by leaf.test.ts (not by any production
	// file); that edge must still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "testutil.ts")}, jestAnalyzer)
	want := []string{filepath.Join(dir, "src", "leaf.test.ts")}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeJestPackageJSONTriggersFullRun(t *testing.T) {
	g, dir := loadSampleJestGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "package.json")}, jestAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run when package.json changes")
	}
}

func TestComputeJestNonSourceFileIsIgnored(t *testing.T) {
	g, dir := loadSampleJestGraph(t)

	res := impact.Compute(g, []string{
		filepath.Join(dir, "src", "leaf.ts"),
		filepath.Join(dir, "README.md"),
	}, jestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{
		filepath.Join(dir, "src", "consumer.test.ts"),
		filepath.Join(dir, "src", "leaf.test.ts"),
		filepath.Join(dir, "src", "mid.test.ts"),
	}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeJestDynamicImportImpact(t *testing.T) {
	g, dir := loadSampleJestGraph(t)

	// dynconsumer.ts reaches dynleaf.ts only through a static-argument
	// dynamic import("./dynleaf"); that edge must still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "dynleaf.ts")}, jestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{filepath.Join(dir, "src", "dynconsumer.test.ts")}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeJestModuleNameMapperImpact(t *testing.T) {
	g, dir := loadSampleJestGraph(t)

	// mapperconsumer.ts reaches libthing.ts only through the "@lib/*"
	// moduleNameMapper alias declared in jest.config.json (not present in
	// tsconfig.json's paths); that edge must still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "libthing.ts")}, jestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{filepath.Join(dir, "src", "mapperconsumer.test.ts")}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func loadSampleJestDynamicGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplejestdynamic"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := jestAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputeJestOpaqueDynamicImportAlwaysIncluded(t *testing.T) {
	g, dir := loadSampleJestDynamicGraph(t)

	// unrelated.ts has no relation to opaque.ts's import(pickModule()), but
	// opaque.test.ts must still run - fastci can't prove the change isn't
	// pickModule()'s runtime target. mixedglob.test.ts must run too: its
	// import(`./missingdir/${name}`) matched no files, so it's uncertain
	// as well, even though its other call (`./plugins/${name}`) resolved.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "unrelated.ts")}, jestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	wantTargets := []string{
		filepath.Join(dir, "src", "mixedglob.test.ts"),
		filepath.Join(dir, "src", "opaque.test.ts"),
		filepath.Join(dir, "src", "unrelated.test.ts"),
	}
	if !reflect.DeepEqual(res.Targets, wantTargets) {
		t.Errorf("Targets = %v, want %v", res.Targets, wantTargets)
	}
	wantChanged := []string{filepath.Join(dir, "src", "unrelated.ts")}
	if !reflect.DeepEqual(res.ChangedTargets, wantChanged) {
		t.Errorf("ChangedTargets = %v, want %v", res.ChangedTargets, wantChanged)
	}
	wantUncertain := []string{
		filepath.Join(dir, "src", "mixedglob.test.ts"),
		filepath.Join(dir, "src", "opaque.test.ts"),
	}
	if !reflect.DeepEqual(res.UncertainTargets, wantUncertain) {
		t.Errorf("UncertainTargets = %v, want %v", res.UncertainTargets, wantUncertain)
	}
}

func TestComputeJestResolvedGlobDoesNotForceInclusion(t *testing.T) {
	g, dir := loadSampleJestDynamicGraph(t)

	// globhit.ts's `./plugins/${name}` resolved to real edges (both plugin
	// files exist), so it's not HasDynamicImport - changing unrelated.ts
	// must not pull globhit.test.ts in.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "unrelated.ts")}, jestAnalyzer)
	for _, target := range res.Targets {
		if target == filepath.Join(dir, "src", "globhit.test.ts") {
			t.Error("globhit.test.ts should not be selected - its dynamic import resolved to real edges, not uncertainty")
		}
	}
}

func loadSamplePytestGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplepytest"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := pytestAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputePytestTransitiveImpactThroughRelativeImports(t *testing.T) {
	g, dir := loadSamplePytestGraph(t)

	// leaf.py <- mid.py (`from .leaf import hello`) <- sub/consumer.py
	// (`from ..mid import greet`, crossing a package boundary) <-
	// test_consumer.py; leaf.py <- bareimporter.py (`from . import leaf`)
	// <- test_bareimporter.py. test_leaf.py and test_mid.py also import
	// their respective modules directly. test_isolated.py must NOT be
	// selected.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "mypkg", "leaf.py")}, pytestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{
		filepath.Join(dir, "tests", "test_bareimporter.py"),
		filepath.Join(dir, "tests", "test_consumer.py"),
		filepath.Join(dir, "tests", "test_leaf.py"),
		filepath.Join(dir, "tests", "test_mid.py"),
	}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputePytestTestOnlyDependencyEdge(t *testing.T) {
	g, dir := loadSamplePytestGraph(t)

	// testutil.py is imported only by test_leaf.py (not by any production
	// file); that edge must still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "mypkg", "testutil.py")}, pytestAnalyzer)
	want := []string{filepath.Join(dir, "tests", "test_leaf.py")}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputePytestPyprojectTriggersFullRun(t *testing.T) {
	g, dir := loadSamplePytestGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "pyproject.toml")}, pytestAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run when pyproject.toml changes")
	}
}

func TestComputePytestConftestTriggersFullRun(t *testing.T) {
	g, dir := loadSamplePytestGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "tests", "conftest.py")}, pytestAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run when conftest.py changes")
	}
}

func TestComputePytestNonSourceFileIsIgnored(t *testing.T) {
	g, dir := loadSamplePytestGraph(t)

	res := impact.Compute(g, []string{
		filepath.Join(dir, "src", "mypkg", "leaf.py"),
		filepath.Join(dir, "README.md"),
	}, pytestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{
		filepath.Join(dir, "tests", "test_bareimporter.py"),
		filepath.Join(dir, "tests", "test_consumer.py"),
		filepath.Join(dir, "tests", "test_leaf.py"),
		filepath.Join(dir, "tests", "test_mid.py"),
	}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func loadSamplePytestDynamicGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplepytestdynamic"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := pytestAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputePytestDynamicImportPropagatesThroughStaticImporter(t *testing.T) {
	g, dir := loadSamplePytestDynamicGraph(t)

	// unrelated.py has no relation to dynloader.py's
	// importlib.import_module(name), but both test_dynloader.py (which
	// imports dynloader.py directly) and test_plainconsumer.py (which
	// imports plainconsumer.py, a static importer of dynloader.py) must
	// still run.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "mypkg", "unrelated.py")}, pytestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	wantTargets := []string{
		filepath.Join(dir, "tests", "test_dynloader.py"),
		filepath.Join(dir, "tests", "test_plainconsumer.py"),
		filepath.Join(dir, "tests", "test_unrelated.py"),
	}
	if !reflect.DeepEqual(res.Targets, wantTargets) {
		t.Errorf("Targets = %v, want %v", res.Targets, wantTargets)
	}
	// ChangedTargets reports the changed node itself (unrelated.py, the file
	// that was actually edited) - not the test file that covers it; it has
	// no test files of its own, so it doesn't appear in Targets.
	wantChanged := []string{filepath.Join(dir, "src", "mypkg", "unrelated.py")}
	if !reflect.DeepEqual(res.ChangedTargets, wantChanged) {
		t.Errorf("ChangedTargets = %v, want %v", res.ChangedTargets, wantChanged)
	}
	wantUncertain := []string{
		filepath.Join(dir, "tests", "test_dynloader.py"),
		filepath.Join(dir, "tests", "test_plainconsumer.py"),
	}
	if !reflect.DeepEqual(res.UncertainTargets, wantUncertain) {
		t.Errorf("UncertainTargets = %v, want %v", res.UncertainTargets, wantUncertain)
	}
}

// TestComputePytestDeletedFileWithOneSurvivingSiblingDoesNotMisattribute
// guards against a real bug: graph.TargetForFile's directory fallback (built
// for Go/Cargo, where many files legitimately share one node) previously
// also applied to pytest's one-node-per-file graphs. Deleting impl.py from a
// directory that then has exactly one file left (public.py, unrelated)
// caused impl.py's deletion to resolve to public.py's node instead of being
// reported as unresolved - silently skipping test_consumer.py, which
// actually imports the deleted impl.py, while only test_public.py ran.
func TestComputePytestDeletedFileWithOneSurvivingSiblingDoesNotMisattribute(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile("pyproject.toml", "[tool.pytest.ini_options]\npythonpath = [\"src\"]\n")
	writeFile("src/mypkg/__init__.py", "")
	// No __init__.py in feature/: an implicit namespace package with
	// exactly one file (public.py) once impl.py is gone.
	writeFile("src/mypkg/feature/public.py", "def pub_fn():\n    return 'public'\n")
	writeFile("tests/test_consumer.py", "from mypkg.feature.impl import impl_fn\n\ndef test_consumer():\n    assert impl_fn() == 'impl'\n")
	writeFile("tests/test_public.py", "from mypkg.feature.public import pub_fn\n\ndef test_public():\n    assert pub_fn() == 'public'\n")

	// impl.py is deliberately never written: this reproduces the
	// post-deletion state Build() sees when impl.py was just `git rm`'d.
	implPy := filepath.Join(dir, "src", "mypkg", "feature", "impl.py")

	g, err := pytestAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, ok := g.TargetForFile(implPy); ok {
		t.Fatal("TargetForFile resolved the deleted impl.py to its surviving sibling public.py - this is the bug")
	}

	res := impact.Compute(g, []string{implPy}, pytestAnalyzer)
	if !res.FullRun {
		t.Errorf("Compute should fall back to a full run when a changed file can't be resolved, got Targets = %v", res.Targets)
	}
}

// buildHandGraph constructs a graph.Graph directly (no analyzer Build())
// for tests that only need to exercise Compute's own logic, independent of
// any language's import resolution: two unrelated nodes x and y, x flagged
// HasDynamicImport, both with test files.
func buildHandGraph(t *testing.T) (g *graph.Graph, yFile string) {
	t.Helper()
	dir := t.TempDir()
	xFile := filepath.Join(dir, "x.go")
	xTestFile := filepath.Join(dir, "x_test.go")
	yFile = filepath.Join(dir, "y.go")
	yTestFile := filepath.Join(dir, "y_test.go")

	g = graph.New()
	x := g.Node("x")
	x.Files = []string{xFile, xTestFile}
	x.HasTestFiles = true
	x.HasDynamicImport = true

	y := g.Node("y")
	y.Files = []string{yFile, yTestFile}
	y.HasTestFiles = true

	g.IndexFiles()
	g.BuildImporters()
	return g, yFile
}

func TestComputeDynamicImportNodeAlwaysIncluded(t *testing.T) {
	g, yFile := buildHandGraph(t)

	// x has no static relationship to y at all; changing y must still pull
	// x in as "uncertain", since x's own dynamic import might resolve to y
	// at runtime.
	res := impact.Compute(g, []string{yFile}, goAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	wantTargets := []string{"x", "y"}
	if !reflect.DeepEqual(res.Targets, wantTargets) {
		t.Errorf("Targets = %v, want %v", res.Targets, wantTargets)
	}
	wantChanged := []string{"y"}
	if !reflect.DeepEqual(res.ChangedTargets, wantChanged) {
		t.Errorf("ChangedTargets = %v, want %v", res.ChangedTargets, wantChanged)
	}
	wantUncertain := []string{"x"}
	if !reflect.DeepEqual(res.UncertainTargets, wantUncertain) {
		t.Errorf("UncertainTargets = %v, want %v", res.UncertainTargets, wantUncertain)
	}
}

func TestComputeWithNoDynamicImportNodesMatchesPreExistingBehavior(t *testing.T) {
	dir := t.TempDir()
	aFile := filepath.Join(dir, "a.go")
	aTestFile := filepath.Join(dir, "a_test.go")
	bFile := filepath.Join(dir, "b.go")
	bTestFile := filepath.Join(dir, "b_test.go")

	g := graph.New()
	a := g.Node("a")
	a.Files = []string{aFile, aTestFile}
	a.HasTestFiles = true
	b := g.Node("b")
	b.Files = []string{bFile, bTestFile}
	b.HasTestFiles = true
	g.IndexFiles()
	g.BuildImporters()

	res := impact.Compute(g, []string{bFile}, goAnalyzer)
	wantTargets := []string{"b"}
	if !reflect.DeepEqual(res.Targets, wantTargets) {
		t.Errorf("Targets = %v, want %v", res.Targets, wantTargets)
	}
	if !reflect.DeepEqual(res.ChangedTargets, wantTargets) {
		t.Errorf("ChangedTargets = %v, want %v", res.ChangedTargets, wantTargets)
	}
	if len(res.UncertainTargets) != 0 {
		t.Errorf("UncertainTargets = %v, want empty", res.UncertainTargets)
	}
}

func loadSampleCargoGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplecargo"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := cargoAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputeCargoTransitiveImpact(t *testing.T) {
	g, dir := loadSampleCargoGraph(t)

	// leaf <- mid <- consumer (path dependencies). Changing leaf should
	// select all three; isolated and testutil (no tests of its own) must
	// not be selected.
	res := impact.Compute(g, []string{filepath.Join(dir, "crates", "leaf", "src", "lib.rs")}, cargoAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{"consumer", "leaf", "mid"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeCargoLeafChangeIsIsolated(t *testing.T) {
	g, dir := loadSampleCargoGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "crates", "isolated", "src", "lib.rs")}, cargoAnalyzer)
	want := []string{"isolated"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeCargoDevDependencyEdge(t *testing.T) {
	g, dir := loadSampleCargoGraph(t)

	// testutil is only a dev-dependency of consumer (used from its
	// tests/it.rs integration test); that edge must still be honored.
	// testutil itself has no tests, so it's not in the result.
	res := impact.Compute(g, []string{filepath.Join(dir, "crates", "testutil", "src", "lib.rs")}, cargoAnalyzer)
	want := []string{"consumer"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeCargoTomlTriggersFullRun(t *testing.T) {
	g, dir := loadSampleCargoGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "crates", "leaf", "Cargo.toml")}, cargoAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run when a crate's Cargo.toml changes")
	}
	want := []string{"consumer", "isolated", "leaf", "mid"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeCargoNonRustFileIsIgnored(t *testing.T) {
	g, dir := loadSampleCargoGraph(t)

	res := impact.Compute(g, []string{
		filepath.Join(dir, "crates", "leaf", "src", "lib.rs"),
		filepath.Join(dir, "README.md"),
	}, cargoAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{"consumer", "leaf", "mid"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func loadSampleVitestGraph(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplevitest"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := vitestAnalyzer.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g, dir
}

func TestComputeVitestTransitiveImpactThroughTsconfigAlias(t *testing.T) {
	g, dir := loadSampleVitestGraph(t)

	// leaf.ts <- mid.ts (relative import) <- consumer.ts (tsconfig path
	// alias @app/mid) <- consumer.test.ts. leaf.test.ts also imports leaf.ts
	// directly. isolated.test.ts must NOT be selected.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "leaf.ts")}, vitestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{
		filepath.Join(dir, "src", "consumer.test.ts"),
		filepath.Join(dir, "src", "leaf.test.ts"),
		filepath.Join(dir, "src", "mid.test.ts"),
	}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeVitestTestOnlyDependencyEdge(t *testing.T) {
	g, dir := loadSampleVitestGraph(t)

	// testutil.ts is imported only by leaf.test.ts (not by any production
	// file); that edge must still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "testutil.ts")}, vitestAnalyzer)
	want := []string{filepath.Join(dir, "src", "leaf.test.ts")}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeVitestConfigTriggersFullRun(t *testing.T) {
	g, dir := loadSampleVitestGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "vitest.config.ts")}, vitestAnalyzer)
	if !res.FullRun {
		t.Fatal("expected a full run when vitest.config.ts changes")
	}
}

func TestComputeVitestNonSourceFileIsIgnored(t *testing.T) {
	g, dir := loadSampleVitestGraph(t)

	res := impact.Compute(g, []string{
		filepath.Join(dir, "src", "leaf.ts"),
		filepath.Join(dir, "README.md"),
	}, vitestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{
		filepath.Join(dir, "src", "consumer.test.ts"),
		filepath.Join(dir, "src", "leaf.test.ts"),
		filepath.Join(dir, "src", "mid.test.ts"),
	}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeVitestDynamicImportImpact(t *testing.T) {
	g, dir := loadSampleVitestGraph(t)

	// dynconsumer.ts reaches dynleaf.ts only through a static-argument
	// dynamic import("./dynleaf"); that edge must still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "dynleaf.ts")}, vitestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{filepath.Join(dir, "src", "dynconsumer.test.ts")}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}
