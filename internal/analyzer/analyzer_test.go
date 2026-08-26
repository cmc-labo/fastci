package analyzer_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer"
	"github.com/hpscript/fastci/internal/graph"
)

// fakeAnalyzer is a minimal analyzer.Analyzer stub for exercising
// analyzer.Detect in isolation, independent of any real language toolchain.
type fakeAnalyzer struct {
	name      string
	detectOK  bool
	detectErr error
}

func (f *fakeAnalyzer) Name() string { return f.name }
func (f *fakeAnalyzer) Detect(dir string) (bool, error) {
	return f.detectOK, f.detectErr
}
func (f *fakeAnalyzer) Build(dir string) (*graph.Graph, error)  { return graph.New(), nil }
func (f *fakeAnalyzer) FullRunFile(absPath string) bool         { return false }
func (f *fakeAnalyzer) Ignorable(absPath string) bool           { return true }
func (f *fakeAnalyzer) AllTargets(dir string) ([]string, error) { return nil, nil }
func (f *fakeAnalyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	return nil
}

func TestDetectSkipsCandidateWhoseDetectErrors(t *testing.T) {
	// Mirrors goanalyzer.Detect failing because no `go` binary is on PATH:
	// that failure must not stop fastci from finding a later, unrelated
	// match (e.g. a Vitest or Jest project).
	broken := &fakeAnalyzer{name: "broken", detectErr: errors.New("exec: \"go\": executable file not found in $PATH")}
	match := &fakeAnalyzer{name: "match", detectOK: true}

	got, err := analyzer.Detect("/some/dir", []analyzer.Analyzer{broken, match})
	if err != nil {
		t.Fatalf("Detect returned an error, want the later match: %v", err)
	}
	if got != match {
		t.Errorf("Detect = %v, want the %q analyzer", got, match.name)
	}
}

func TestDetectReportsAllErrorsWhenNothingMatches(t *testing.T) {
	broken := &fakeAnalyzer{name: "broken", detectErr: errors.New("boom")}
	unmatched := &fakeAnalyzer{name: "unmatched"}

	_, err := analyzer.Detect("/some/dir", []analyzer.Analyzer{broken, unmatched})
	if err == nil {
		t.Fatal("Detect = nil error, want an error since no candidate matched")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("Detect error = %q, want it to mention the broken candidate's error", err.Error())
	}
}

func TestDetectNoMatchWithoutErrors(t *testing.T) {
	unmatched := &fakeAnalyzer{name: "unmatched"}

	_, err := analyzer.Detect("/some/dir", []analyzer.Analyzer{unmatched})
	if err == nil {
		t.Fatal("Detect = nil error, want an error since no candidate matched")
	}
	if !strings.Contains(err.Error(), "no supported project detected") {
		t.Errorf("Detect error = %q, want it to mention no supported project was detected", err.Error())
	}
}
