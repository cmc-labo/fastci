package jestanalyzer_test

import (
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/jestanalyzer"
)

func sampleJestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplejest"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDetect(t *testing.T) {
	a := jestanalyzer.New()
	ok, err := a.Detect(sampleJestDir(t))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Error("Detect = false, want true")
	}
}

func TestDetectRejectsNonJestDir(t *testing.T) {
	a := jestanalyzer.New()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplemod"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := a.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true for a non-Jest (Go) directory, want false")
	}
}

func TestBuildResolvesRelativeAndTsconfigAliasImports(t *testing.T) {
	dir := sampleJestDir(t)
	a := jestanalyzer.New()
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

func TestBuildResolvesStaticDynamicImport(t *testing.T) {
	dir := sampleJestDir(t)
	a := jestanalyzer.New()
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

func TestBuildResolvesModuleNameMapperAlias(t *testing.T) {
	dir := sampleJestDir(t)
	a := jestanalyzer.New()
	g, err := a.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	libthing := filepath.Join(dir, "src", "libthing.ts")
	mapperconsumer := filepath.Join(dir, "src", "mapperconsumer.ts")

	if _, ok := g.Nodes[libthing]; !ok {
		t.Fatalf("missing node for %s", libthing)
	}
	if !g.Nodes[mapperconsumer].Imports[libthing] {
		t.Error(`mapperconsumer.ts should import libthing.ts via the "@lib/*" moduleNameMapper alias in jest.config.json`)
	}
}

func TestBuildIgnoresNodeModules(t *testing.T) {
	dir := sampleJestDir(t)
	a := jestanalyzer.New()
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
