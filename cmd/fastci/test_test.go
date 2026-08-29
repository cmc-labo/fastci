package main

import (
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/goanalyzer"
	"github.com/hpscript/fastci/internal/graph"
)

// buildGraphWithFiles constructs a minimal graph where the given nodes each
// own the listed number of files, for exercising fullRunThresholdReason's
// file-counting independent of any real analyzer's Build().
func buildGraphWithFiles(t *testing.T, filesPerNode map[string]int) *graph.Graph {
	t.Helper()
	g := graph.New()
	for id, n := range filesPerNode {
		files := make([]string, n)
		for i := range files {
			files[i] = id + "/f" + string(rune('a'+i)) + ".go"
		}
		g.Node(id).Files = files
	}
	return g
}

func TestFullRunThresholdReasonBelowThreshold(t *testing.T) {
	g := buildGraphWithFiles(t, map[string]int{"pkg": 10})
	changed := []string{"pkg/fa.go"} // 1/10 = 10%
	a := goanalyzer.New()

	if _, ok := fullRunThresholdReason(g, changed, a, 20); ok {
		t.Error("fullRunThresholdReason = true, want false (10% is below the 20% threshold)")
	}
}

func TestFullRunThresholdReasonAtOrAboveThreshold(t *testing.T) {
	g := buildGraphWithFiles(t, map[string]int{"pkg": 10})
	changed := []string{"pkg/fa.go", "pkg/fb.go"} // 2/10 = 20%
	a := goanalyzer.New()

	reason, ok := fullRunThresholdReason(g, changed, a, 20)
	if !ok {
		t.Fatal("fullRunThresholdReason = false, want true (20% meets the 20% threshold)")
	}
	if reason == "" {
		t.Error("fullRunThresholdReason returned an empty reason string")
	}
}

func TestFullRunThresholdReasonIgnoresNonTrackedFiles(t *testing.T) {
	g := buildGraphWithFiles(t, map[string]int{"pkg": 10})
	// README.md is Ignorable for goanalyzer (not a .go file), so it must not
	// count toward the changed-file numerator even though it's in the diff.
	changed := []string{"README.md"}
	a := goanalyzer.New()

	if _, ok := fullRunThresholdReason(g, changed, a, 1); ok {
		t.Error("fullRunThresholdReason = true, want false (the only changed file is Ignorable, not a tracked source file)")
	}
}

func TestFullRunThresholdReasonEmptyGraph(t *testing.T) {
	g := graph.New()
	a := goanalyzer.New()

	if _, ok := fullRunThresholdReason(g, []string{"anything.go"}, a, 1); ok {
		t.Error("fullRunThresholdReason = true, want false for an empty graph (no tracked files to divide by)")
	}
}
