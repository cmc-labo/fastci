package cargoanalyzer_test

import (
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/cargoanalyzer"
)

func sampleCargoDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplecargo"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDetect(t *testing.T) {
	a := cargoanalyzer.New()
	ok, err := a.Detect(sampleCargoDir(t))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Error("Detect = false, want true")
	}
}

func TestDetectRejectsNonCargoDir(t *testing.T) {
	a := cargoanalyzer.New()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplemod"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := a.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true for a non-Cargo (Go) directory, want false")
	}
}

func TestBuildForwardAndReverseEdges(t *testing.T) {
	a := cargoanalyzer.New()
	g, err := a.Build(sampleCargoDir(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantNodes := []string{"leaf", "mid", "consumer", "testutil", "isolated"}
	for _, n := range wantNodes {
		if _, ok := g.Nodes[n]; !ok {
			t.Errorf("missing node %s", n)
		}
	}

	if !g.Nodes["mid"].Imports["leaf"] {
		t.Error("mid should depend on leaf")
	}
	if !g.Nodes["consumer"].Imports["mid"] {
		t.Error("consumer should depend on mid")
	}
	if !g.Nodes["consumer"].Imports["testutil"] {
		t.Error("consumer should depend on testutil (dev-dependency)")
	}
	if len(g.Nodes["leaf"].Imports) != 0 {
		t.Errorf("leaf should have no dependencies, got %v", g.Nodes["leaf"].Imports)
	}
	if len(g.Nodes["isolated"].Imports) != 0 {
		t.Errorf("isolated should have no dependencies, got %v", g.Nodes["isolated"].Imports)
	}

	for _, n := range []string{"leaf", "mid", "consumer", "isolated"} {
		if !g.Nodes[n].HasTestFiles {
			t.Errorf("%s should be classified as having tests", n)
		}
	}
	if g.Nodes["testutil"].HasTestFiles {
		t.Error("testutil should not be classified as having tests")
	}
}

func TestTargetForFile(t *testing.T) {
	dir := sampleCargoDir(t)
	a := cargoanalyzer.New()
	g, err := a.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	f := filepath.Join(dir, "crates", "mid", "src", "lib.rs")
	target, ok := g.TargetForFile(f)
	if !ok || target != "mid" {
		t.Errorf("TargetForFile(%s) = %q, %v; want mid, true", f, target, ok)
	}
}
