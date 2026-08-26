// Package analyzer defines the interface each language/test-runner
// implementation (Go, Jest, ...) must satisfy so that fastci's impact
// analysis and CLI can stay language-agnostic.
package analyzer

import (
	"context"
	"fmt"
	"strings"

	"github.com/hpscript/fastci/internal/graph"
)

// Analyzer builds a dependency graph for a project and knows how to run its
// tests.
type Analyzer interface {
	// Name identifies the analyzer for logging, e.g. "go", "jest".
	Name() string

	// Detect reports whether dir looks like a project root this analyzer
	// can handle (e.g. a go.mod/go.work, or a package.json with Jest
	// configured).
	Detect(dir string) (bool, error)

	// Build builds the dependency graph for the project rooted at dir.
	Build(dir string) (*graph.Graph, error)

	// FullRunFile reports whether a changed file (absolute path) should
	// force a full test run regardless of graph resolution - e.g. go.mod,
	// package.json, a lockfile, or a test-runner config file whose effects
	// aren't captured by the import graph.
	FullRunFile(absPath string) bool

	// Ignorable reports whether a changed file that didn't resolve to any
	// graph node can safely be treated as having no effect on tests (e.g.
	// a README or other file outside the analyzer's tracked source set),
	// as opposed to being an unresolvable source file that should force a
	// full run out of caution.
	Ignorable(absPath string) bool

	// AllTargets returns the target set for a full/--all run, without
	// requiring a graph to be built first. An empty result means "run
	// everything" is the test runner's own default behavior.
	AllTargets(dir string) ([]string, error)

	// RunTests executes the tests for targets (as produced by impact
	// analysis or AllTargets), forwarding extraArgs to the underlying test
	// runner.
	RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error
}

// Detect tries each candidate's Detect method against dir in order and
// returns the first match. A candidate whose Detect call itself errors -
// e.g. goanalyzer's `go env GOWORK` lookup when no `go` binary is on PATH -
// doesn't abort detection for every other, unrelated candidate; it's
// treated as "this one doesn't match" and folded into the final error only
// if no candidate matches at all.
func Detect(dir string, candidates []Analyzer) (Analyzer, error) {
	var detectErrs []string
	for _, a := range candidates {
		ok, err := a.Detect(dir)
		if err != nil {
			detectErrs = append(detectErrs, fmt.Sprintf("%s: %v", a.Name(), err))
			continue
		}
		if ok {
			return a, nil
		}
	}
	if len(detectErrs) > 0 {
		return nil, fmt.Errorf("no supported project detected in %s (%s)", dir, strings.Join(detectErrs, "; "))
	}
	return nil, fmt.Errorf("no supported project detected in %s", dir)
}
