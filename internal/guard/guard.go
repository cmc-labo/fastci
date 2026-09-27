// Package guard implements fastci's Phase 3 "fastci guard" feature:
// supply-chain security scanning. Most Checkers orchestrate an ecosystem's
// own official, trusted scanner (govulncheck, npm/pnpm/yarn audit,
// pip-audit, cargo-audit) - the same "delegate to the real toolchain"
// approach the rest of fastci already takes for go/packages, esbuild, and
// cargo metadata, rather than reimplementing something a language's own
// tooling already does authoritatively. LifecycleScripts and
// CargoBuildScripts are the exceptions, since no equivalent official tool
// exists for either specific check - see their own doc comments.
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
//
// This assumes the underlying tool uses the simplest possible convention:
// exit 0 means clean, any other exit code means a finding. Several real
// scanners don't: govulncheck reserves a specific exit code (3) for an
// actual finding and uses others (most commonly 1) for the tool itself
// failing - most often, in practice, a transient failure to fetch its
// vulnerability database - and npm/pnpm/yarn audit, pip-audit, and
// cargo-audit all reuse the very same exit code for both a real finding
// and a network/infra failure fetching advisory data, distinguishable
// only by a recognizable message in their own output. A checker for any
// of those (see govulncheck.go, jsaudit.go, pipaudit.go, cargoaudit.go)
// uses runCmd directly instead, so it can apply its own tool's actual
// convention rather than this one - reporting a network hiccup as
// FoundIssues would be a real, misleading false positive.
func runChecker(ctx context.Context, name, dir string, argv []string) (Result, error) {
	output, exitCode, err := runCmd(ctx, name, dir, argv, nil)
	if err != nil {
		return Result{}, err
	}
	return Result{CheckerName: name, Output: output, FoundIssues: exitCode != 0}, nil
}

// runCmd runs argv in dir (with extraEnv appended to the current
// process's own environment, if non-empty) and returns its combined
// stdout+stderr and exit code (0 for success). err is non-nil only if the
// command couldn't even be started (a missing binary, a permission
// error, ...) - an ordinary non-zero exit is reported via exitCode, not
// err, since interpreting what a particular exit code (or
// exit-code-plus-message) actually means is each caller's own job; see
// runChecker's doc comment for why that varies by tool.
func runCmd(ctx context.Context, name, dir string, argv []string, extraEnv []string) (output string, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	output = buf.String()
	if runErr == nil {
		return output, 0, nil
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return output, exitErr.ExitCode(), nil
	}
	return output, -1, fmt.Errorf("%s: %w", name, runErr)
}

// Checkers lists every built-in Checker, in a fixed, stable order.
func Checkers() []Checker {
	return []Checker{
		GoVulnCheck{},
		JSAudit{},
		LifecycleScripts{},
		PipAudit{},
		CargoAudit{},
		CargoBuildScripts{},
	}
}
