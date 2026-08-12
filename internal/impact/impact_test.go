package impact_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer"
	"github.com/hpscript/fastci/internal/analyzer/goanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/jestanalyzer"
	"github.com/hpscript/fastci/internal/analyzer/pytestanalyzer"
	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/impact"
)

var goAnalyzer analyzer.Analyzer = goanalyzer.New()
var jestAnalyzer analyzer.Analyzer = jestanalyzer.New()
var pytestAnalyzer analyzer.Analyzer = pytestanalyzer.New()

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
	// test_consumer.py. test_leaf.py and test_mid.py also import their
	// respective modules directly. test_isolated.py must NOT be selected.
	res := impact.Compute(g, []string{filepath.Join(dir, "src", "mypkg", "leaf.py")}, pytestAnalyzer)
	if res.FullRun {
		t.Fatalf("unexpected full run, reasons: %v", res.FullRunReasons)
	}
	want := []string{
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
		filepath.Join(dir, "tests", "test_consumer.py"),
		filepath.Join(dir, "tests", "test_leaf.py"),
		filepath.Join(dir, "tests", "test_mid.py"),
	}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}
