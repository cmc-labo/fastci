package impact_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hpscript/fastci/internal/depgraph"
	"github.com/hpscript/fastci/internal/impact"
)

func loadSampleGraph(t *testing.T) (*depgraph.Graph, string) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "samplemod"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := depgraph.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return g, dir
}

func TestComputeTransitiveImpact(t *testing.T) {
	g, dir := loadSampleGraph(t)

	// pkgc is a leaf dependency of pkgb, which is a dependency of pkga.
	// Changing it should select all three.
	res := impact.Compute(g, []string{filepath.Join(dir, "pkgc", "pkgc.go")})
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
	res := impact.Compute(g, []string{filepath.Join(dir, "pkga", "pkga.go")})
	want := []string{"samplemod/pkga"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeTestOnlyDependencyEdge(t *testing.T) {
	g, dir := loadSampleGraph(t)

	// pkge depends on pkgd only from its _test.go file; that edge must
	// still be honored.
	res := impact.Compute(g, []string{filepath.Join(dir, "pkgd", "pkgd.go")})
	want := []string{"samplemod/pkgd", "samplemod/pkge"}
	if !reflect.DeepEqual(res.Targets, want) {
		t.Errorf("Targets = %v, want %v", res.Targets, want)
	}
}

func TestComputeGoModTriggersFullRun(t *testing.T) {
	g, dir := loadSampleGraph(t)

	res := impact.Compute(g, []string{filepath.Join(dir, "go.mod")})
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
	})
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

	res := impact.Compute(g, []string{filepath.Join(dir, "pkgf", "ghost.go")})
	if !res.FullRun {
		t.Fatal("expected a full run for an unresolvable .go file")
	}
}
