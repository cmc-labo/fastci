// Package debug is fastci's own, deliberately minimal debug-logging
// mechanism: a single environment variable, FASTCI_DEBUG, gates a stream
// of diagnostic lines to stderr that are never shown otherwise - not a
// logging framework (no levels beyond on/off, no structured fields, no
// output destinations beyond stderr), since fastci's normal output
// already goes straight to the terminal/CI log and a real framework
// would be solving a problem this tool doesn't have.
//
// This exists specifically for the gap `-v`/`--verbose` doesn't cover:
// -v explains *impact analysis's own reasoning* (which files changed,
// why a target was or wasn't selected) - it was never meant to show the
// exact subprocess command lines, working directories, and timings
// behind every `go list`/`cargo metadata`/`git`/test-runner invocation
// fastci makes along the way. Those are exactly what's worth seeing when
// something's slow, hangs, or fails in a way the ordinary error message
// doesn't explain - found, in practice, to matter most for exactly the
// class of bug this project has repeatedly hit: a scanner or test runner
// behaving unexpectedly against one specific real-world project layout,
// where "what command did fastci actually run, and how long did it take"
// is the first real question to answer.
package debug

import (
	"fmt"
	"os"
	"time"
)

// Enabled reports whether FASTCI_DEBUG is currently set - checked fresh
// on every call (like FASTCI_REMOTE_CACHE_URL/FASTCI_BASE_BRANCH
// elsewhere in fastci), not cached at process startup, so tests can
// toggle it with t.Setenv and a long-running caller could too, in
// principle. Exported so a caller can skip assembling an expensive debug
// message entirely when it's off, rather than relying on Logf's own
// (cheap, but not free) check.
func Enabled() bool { return os.Getenv("FASTCI_DEBUG") != "" }

// Logf prints a debug line to stderr, consistently prefixed, only when
// FASTCI_DEBUG is set - a no-op otherwise. format should not include a
// trailing newline.
func Logf(format string, args ...any) {
	if !Enabled() {
		return
	}
	fmt.Fprintf(os.Stderr, "fastci: [debug] "+format+"\n", args...)
}

// Command logs argv and dir before a subprocess runs, and returns a func
// to call after it finishes (however it finishes - success, failure, or
// panic recovery further up the stack is not this package's concern) to
// log how long it took. A no-op pair when FASTCI_DEBUG isn't set, so the
// time.Now() call itself is also skipped in the common case:
//
//	done := debug.Command("go", []string{"go", "list", "./..."}, dir)
//	err := cmd.Run()
//	done()
func Command(name string, argv []string, dir string) (done func()) {
	if !Enabled() {
		return func() {}
	}
	Logf("%s: running %v in %q", name, argv, dir)
	start := time.Now()
	return func() {
		Logf("%s: finished in %s", name, time.Since(start))
	}
}
