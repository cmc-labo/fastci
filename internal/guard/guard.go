// Package guard implements fastci's Phase 3 "fastci guard" feature:
// supply-chain security scanning. Most Checkers orchestrate an ecosystem's
// own official, trusted scanner (govulncheck, npm/pnpm/yarn audit,
// pip-audit, cargo-audit) - the same "delegate to the real toolchain"
// approach the rest of fastci already takes for go/packages, esbuild, and
// cargo metadata, rather than reimplementing something a language's own
// tooling already does authoritatively. LifecycleScripts is the one
// exception, since no equivalent official tool exists for that specific
// check - see its own doc comment.
//
// A Checker's Run doesn't try to parse its underlying tool's findings into
// a structured shape: vulnerability report formats vary and change across
// tool versions, and a fragile parser that silently mis-reads a new format
// as "no issues found" would be far worse than just surfacing the tool's
// own text output as-is and trusting its exit code.
package guard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Result is the outcome of running one Checker.
type Result struct {
	CheckerName string
	FoundIssues bool   // the underlying tool's exit code indicates a problem.
	Output      string // the tool's own combined stdout+stderr, unparsed.
}

// Checker wraps one ecosystem's official vulnerability/audit tool.
type Checker interface {
	// Name identifies the checker for logging, e.g. "govulncheck".
	Name() string

	// Detect reports whether dir looks like a project this checker applies
	// to (e.g. a go.mod for govulncheck).
	Detect(dir string) (bool, error)

	// BinaryAvailable reports whether the underlying tool is installed and
	// runnable for dir's project (which package manager's audit applies can
	// depend on which lockfile is present). When it isn't, installHint is a
	// human-readable command the user can run to install it.
	BinaryAvailable(dir string) (ok bool, installHint string)

	// Run executes the checker against dir. The returned error is only for
	// infra-level failures (the tool couldn't be started, or produced
	// output Run couldn't even read) - "the tool ran and found
	// vulnerabilities" is reported via Result.FoundIssues, not err, mirroring
	// how a failing test run isn't itself a fastci bug.
	Run(ctx context.Context, dir string) (Result, error)
}

// runChecker runs argv in dir and turns its outcome into a Result: an
// ordinary non-zero exit (the tool ran to completion and reported a
// problem) sets FoundIssues; a failure to even start the tool (missing
// binary, permission error, ...) is returned as err instead, since that's
// an infra problem for the caller to report distinctly from "vulnerabilities
// were found".
func runChecker(ctx context.Context, name, dir string, argv []string) (Result, error) {
	return runCheckerEnv(ctx, name, dir, argv, nil)
}

// runCheckerEnv is runChecker with extraEnv appended to the child's
// environment (on top of the current process's own, matching
// exec.Cmd's normal default when Env is left nil) - for a checker that
// needs to steer its underlying tool beyond argv, e.g. pip-audit's
// PIPAPI_PYTHON_LOCATION (see pipaudit.go).
func runCheckerEnv(ctx context.Context, name, dir string, argv []string, extraEnv []string) (Result, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	res := Result{CheckerName: name}
	err := cmd.Run()
	res.Output = buf.String()
	if err == nil {
		return res, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		res.FoundIssues = true
		return res, nil
	}
	return res, fmt.Errorf("%s: %w", name, err)
}

// Checkers lists every built-in Checker, in a fixed, stable order.
func Checkers() []Checker {
	return []Checker{
		GoVulnCheck{},
		JSAudit{},
		LifecycleScripts{},
		PipAudit{},
		CargoAudit{},
	}
}
