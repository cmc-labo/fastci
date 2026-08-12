package pytestanalyzer_test

import (
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/pytestanalyzer"
)

func samplePytestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplepytest"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDetect(t *testing.T) {
	a := pytestanalyzer.New()
	ok, err := a.Detect(samplePytestDir(t))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Error("Detect = false, want true")
	}
}

func TestDetectRejectsNonPytestDir(t *testing.T) {
	a := pytestanalyzer.New()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "samplemod"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := a.Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Error("Detect = true for a non-pytest (Go) directory, want false")
	}
}

func TestBuildResolvesRelativeAndAbsoluteImports(t *testing.T) {
	dir := samplePytestDir(t)
	a := pytestanalyzer.New()
	g, err := a.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	leaf := filepath.Join(dir, "src", "mypkg", "leaf.py")
	mid := filepath.Join(dir, "src", "mypkg", "mid.py")
	consumer := filepath.Join(dir, "src", "mypkg", "sub", "consumer.py")
	testutil := filepath.Join(dir, "src", "mypkg", "testutil.py")
	testLeaf := filepath.Join(dir, "tests", "test_leaf.py")
	testMid := filepath.Join(dir, "tests", "test_mid.py")
	testConsumer := filepath.Join(dir, "tests", "test_consumer.py")

	for _, f := range []string{leaf, mid, consumer, testutil, testLeaf, testMid, testConsumer} {
		if _, ok := g.Nodes[f]; !ok {
			t.Errorf("missing node for %s", f)
		}
	}

	if !g.Nodes[mid].Imports[leaf] {
		t.Error("mid.py should import leaf.py (relative import `from .leaf import hello`)")
	}
	if !g.Nodes[consumer].Imports[mid] {
		t.Error("sub/consumer.py should import mid.py (relative import `from ..mid import greet`, crossing a package boundary)")
	}
	if !g.Nodes[testLeaf].Imports[leaf] {
		t.Error("test_leaf.py should import leaf.py (absolute import `from mypkg.leaf import hello`)")
	}
	if !g.Nodes[testLeaf].Imports[testutil] {
		t.Error("test_leaf.py should import testutil.py")
	}
	if !g.Nodes[testConsumer].Imports[consumer] {
		t.Error("test_consumer.py should import sub/consumer.py")
	}

	for _, f := range []string{testLeaf, testMid, testConsumer} {
		if !g.Nodes[f].HasTestFiles {
			t.Errorf("%s should be classified as a test file", f)
		}
	}
	if g.Nodes[leaf].HasTestFiles {
		t.Error("leaf.py should not be classified as a test file")
	}
}
