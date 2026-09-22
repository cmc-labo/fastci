package vitestanalyzer_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/vitestanalyzer"
)

func sampleVitestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplevitest"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDetect(t *testing.T) {
	a := vitestanalyzer.New()
	ok, err := a.Detect(sampleVitestDir(t))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Error("Detect = false, want true")
	}
}

func TestDetectRejectsNonVitestDir(t *testing.T) {
	a := vitestanalyzer.New()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplemod"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := a.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true for a non-Vitest (Go) directory, want false")
	}
}

func TestDetectRejectsPlainJestDir(t *testing.T) {
	a := vitestanalyzer.New()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplejest"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := a.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true for a Jest (not Vitest) directory, want false")
	}
}

func TestBuildResolvesRelativeAndTsconfigAliasImports(t *testing.T) {
	dir := sampleVitestDir(t)
	a := vitestanalyzer.New()
	g, err := a.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	leaf := filepath.Join(dir, "src", "leaf.ts")
	leafTest := filepath.Join(dir, "src", "leaf.test.ts")
	mid := filepath.Join(dir, "src", "mid.ts")
	midTest := filepath.Join(dir, "src", "mid.test.ts")
	consumer := filepath.Join(dir, "src", "consumer.ts")
	consumerTest := filepath.Join(dir, "src", "consumer.test.ts")
	testutil := filepath.Join(dir, "src", "testutil.ts")

	for _, f := range []string{leaf, leafTest, mid, midTest, consumer, consumerTest, testutil} {
		if _, ok := g.Nodes[f]; !ok {
			t.Errorf("missing node for %s", f)
		}
	}

	if !g.Nodes[mid].Imports[leaf] {
		t.Error("mid.ts should import leaf.ts (relative import)")
	}
	if !g.Nodes[consumer].Imports[mid] {
		t.Error("consumer.ts should import mid.ts (resolved via tsconfig path alias @app/mid)")
	}
	if !g.Nodes[leafTest].Imports[testutil] {
		t.Error("leaf.test.ts should import testutil.ts")
	}

	for _, f := range []string{leafTest, midTest, consumerTest} {
		if !g.Nodes[f].HasTestFiles {
			t.Errorf("%s should be classified as a test file", f)
		}
	}
	if g.Nodes[leaf].HasTestFiles {
		t.Error("leaf.ts should not be classified as a test file")
	}
}

// TestBuildResolvesViteResolveAliasImport is a real integration test
// (requires "node" on PATH - it skips cleanly otherwise) proving the Vite
// `resolve.alias` config in testdata/samplevitest/vitest.config.ts (a
// `path.resolve(__dirname, "src")` alias, the idiom used by essentially
// every real Vite project) actually gets resolved: this fixture's
// viteconsumer.ts imports leaf.ts only through the "@viteonly" alias, with
// no relative or tsconfig-path route to it at all, so this edge can only
// exist in the graph if resolveViteAliases actually executed the config
// and fed the result into esbuild's own Alias option.
func TestBuildResolvesViteResolveAliasImport(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed - skipping this real integration test")
	}

	dir := sampleVitestDir(t)
	a := vitestanalyzer.New()
	g, err := a.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	leaf := filepath.Join(dir, "src", "leaf.ts")
	viteconsumer := filepath.Join(dir, "src", "viteconsumer.ts")

	if _, ok := g.Nodes[viteconsumer]; !ok {
		t.Fatalf("missing node for %s", viteconsumer)
	}
	if !g.Nodes[viteconsumer].Imports[leaf] {
		t.Error(`viteconsumer.ts should import leaf.ts, resolved via the "@viteonly" Vite resolve.alias in vitest.config.ts`)
	}
}

func TestBuildResolvesStaticDynamicImport(t *testing.T) {
	dir := sampleVitestDir(t)
	a := vitestanalyzer.New()
	g, err := a.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	dynleaf := filepath.Join(dir, "src", "dynleaf.ts")
	dynconsumer := filepath.Join(dir, "src", "dynconsumer.ts")

	if _, ok := g.Nodes[dynleaf]; !ok {
		t.Fatalf("missing node for %s", dynleaf)
	}
	if !g.Nodes[dynconsumer].Imports[dynleaf] {
		t.Error(`dynconsumer.ts should import dynleaf.ts via a static-argument dynamic import("./dynleaf")`)
	}
}

func TestBuildIgnoresNodeModules(t *testing.T) {
	dir := sampleVitestDir(t)
	a := vitestanalyzer.New()
	g, err := a.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for id := range g.Nodes {
		if filepath.Base(filepath.Dir(id)) == "node_modules" {
			t.Errorf("node_modules should not appear in the graph, got %s", id)
		}
	}
}
