package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/hpscript/fastci/internal/guard"
)

// checkerTimeout bounds a single Checker.Run call. These are ordinary
// vulnerability/advisory scans, not test suites: a well-behaved run takes
// seconds to at most a couple of minutes (a fresh advisory-database
// fetch, most commonly - see e.g. cargoaudit.go), so this is generous,
// not tight. It exists as a last-resort safety net, not the primary fix,
// for a checker's underlying tool hanging outright - which does happen in
// practice, not just in theory (a real pnpm bug: on a network failure, it
// can leave a child process holding stdout/stderr open after its own
// top-level process has already exited) - so that a single hung scanner
// can never block the rest of `fastci guard`, or the CI job running it,
// forever.
const checkerTimeout = 5 * time.Minute

func newGuardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guard",
		Short: "Scan for known vulnerabilities using each ecosystem's own official scanner",
		Long: `guard runs every applicable ecosystem's own official, trusted vulnerability
scanner against the working directory - govulncheck for Go, npm/pnpm/yarn
audit for JavaScript/TypeScript, pip-audit for Python, cargo-audit for
Rust - and reports what each one found. It doesn't implement any
vulnerability detection itself, and it detects every applicable ecosystem
independently (unlike "fastci test", which picks a single project type):
a monorepo with both a go.mod and a package.json gets both scanners run.

Exits non-zero if any scanner reports a vulnerability. A scanner whose
underlying tool isn't installed is skipped with an install hint printed -
that's reported, but doesn't by itself make guard exit non-zero, since it's
"couldn't check" rather than "checked and found a problem".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGuard(cmd)
		},
	}
	return cmd
}

func runGuard(cmd *cobra.Command) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	var applicable []guard.Checker
	for _, c := range guard.Checkers() {
		ok, err := c.Detect(cwd)
		if err != nil {
			return fmt.Errorf("guard %s: detecting: %w", c.Name(), err)
		}
		if ok {
			applicable = append(applicable, c)
		}
	}

	if len(applicable) == 0 {
		fmt.Println("fastci: no recognized project type in this directory - nothing to scan")
		return nil
	}

	var (
		anyIssues bool
		anySkips  bool
	)
	for i, c := range applicable {
		if i > 0 {
			fmt.Println()
		}
		if ok, hint := c.BinaryAvailable(cwd); !ok {
			fmt.Printf("fastci: %s: skipped - %s\n", c.Name(), hint)
			anySkips = true
			continue
		}

		fmt.Printf("fastci: running %s...\n", c.Name())
		ctx, cancel := context.WithTimeout(cmd.Context(), checkerTimeout)
		res, err := c.Run(ctx, cwd)
		cancel()
		if err != nil {
			// An infra-level failure (a network hiccup fetching advisory
			// data, most commonly - see e.g. govulncheck.go) is "couldn't
			// check", the same as the tool not being installed at all -
			// not a reason to abort every other checker too. A monorepo
			// with, say, both a go.mod and a package.json should still get
			// its npm audit run even if govulncheck's own network call
			// failed.
			fmt.Printf("fastci: %s: could not complete - %v\n", c.Name(), err)
			anySkips = true
			continue
		}
		if res.Output != "" {
			fmt.Println(res.Output)
		}
		if res.FoundIssues {
			fmt.Printf("fastci: %s reported issues (see above)\n", c.Name())
			anyIssues = true
		} else {
			fmt.Printf("fastci: %s: no issues found\n", c.Name())
		}
	}

	fmt.Println()
	switch {
	case anyIssues:
		return fmt.Errorf("one or more scanners reported vulnerabilities")
	case anySkips:
		fmt.Println("fastci: no vulnerabilities found by the scanners that ran, but at least one applicable scanner was skipped - see above")
	default:
		fmt.Println("fastci: no vulnerabilities found")
	}
	return nil
}
