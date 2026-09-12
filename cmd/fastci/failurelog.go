package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/hpscript/fastci/internal/analyzer"
	"github.com/hpscript/fastci/internal/runner"
)

// failureLogMaxOutput caps how much of a run's combined stdout/stderr gets
// persisted (and later sent to the Anthropic API by `fastci analyze`): the
// most relevant part of a test failure - the actual assertion/traceback -
// is almost always near the end, so the *tail* is kept when output is
// larger than this.
const failureLogMaxOutput = 50_000

// ANSI escape sequences (SGR color codes, OSC hyperlinks, character-set
// selection) that survive into the captured copy now that captureOutput
// gives an interactive run's child a real pty (see captureOutputPTY) to
// keep its live-forwarded colors intact. They render as illegible
// \x1b[...m noise once written to JSON or sent as a plain-text prompt to
// the Anthropic API, so they're stripped from the *persisted/analyzed*
// copy only - the live terminal forward is untouched and stays colored.
var (
	ansiOSC     = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	ansiCSI     = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	ansiCharset = regexp.MustCompile(`\x1b[()][A-Za-z0-9]`)
)

func stripANSI(s []byte) []byte {
	s = ansiOSC.ReplaceAll(s, nil)
	s = ansiCSI.ReplaceAll(s, nil)
	s = ansiCharset.ReplaceAll(s, nil)
	return s
}

// truncateUTF8Tail returns the last max bytes of s, advanced past any
// leading UTF-8 continuation bytes so a plain byte-offset cut never splits
// a multi-byte rune in half - which would otherwise leave a stray invalid
// leading fragment (silently rendered as one "�" replacement
// character by both encoding/json and the Anthropic API request built from
// it - not a crash, but real, avoidable corruption of the captured text
// right at the cut point).
func truncateUTF8Tail(s []byte, max int) []byte {
	s = s[len(s)-max:]
	i := 0
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// failureLog is the on-disk record `fastci test` leaves behind when a run
// fails, for `fastci analyze` to pick up afterward.
type failureLog struct {
	Timestamp time.Time `json:"timestamp"`
	Analyzer  string    `json:"analyzer"`
	Targets   []string  `json:"targets"`
	Command   string    `json:"command"`
	Error     string    `json:"error"`
	Output    string    `json:"output"`
	Truncated bool      `json:"truncated"`
}

func failureLogPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".fastci-cache", "last-failure.json")
}

// runAndRecord runs a.RunTests, capturing a copy of everything the test
// runner itself writes to stdout/stderr while it's still streamed live to
// the user exactly as before (fastci's own banner lines printed before
// this call aren't part of the capture). On failure, it's persisted as a
// failureLog for a later `fastci analyze` to read; on success, any
// previous failure log is cleared so `analyze` doesn't describe a problem
// that's already fixed.
func runAndRecord(ctx context.Context, repoRoot string, a analyzer.Analyzer, cwd string, targets []string, extraArgs []string) error {
	command := a.Name()
	if len(extraArgs) > 0 {
		command = fmt.Sprintf("%s (extra args: %v)", command, extraArgs)
	}

	output, err := captureOutput(func() error {
		return a.RunTests(ctx, cwd, targets, extraArgs)
	})

	if err == nil {
		os.Remove(failureLogPath(repoRoot)) // best-effort: stale failure log would mislead `analyze`.
		return nil
	}

	output = stripANSI(output)

	truncated := false
	if len(output) > failureLogMaxOutput {
		output = truncateUTF8Tail(output, failureLogMaxOutput)
		truncated = true
	}
	rec := failureLog{
		Timestamp: time.Now(),
		Analyzer:  a.Name(),
		Targets:   targets,
		Command:   command,
		Error:     err.Error(),
		Output:    string(output),
		Truncated: truncated,
	}
	if writeErr := writeFailureLog(repoRoot, rec); writeErr != nil {
		fmt.Fprintf(os.Stderr, "fastci: could not save failure details for `fastci analyze`: %v\n", writeErr)
	}
	return err
}

func writeFailureLog(repoRoot string, rec failureLog) error {
	path := failureLogPath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	gitignore := filepath.Join(filepath.Dir(path), ".gitignore")
	if _, err := os.Stat(gitignore); os.IsNotExist(err) {
		_ = os.WriteFile(gitignore, []byte("*\n"), 0o644)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "last-failure-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func readFailureLog(repoRoot string) (failureLog, error) {
	var rec failureLog
	data, err := os.ReadFile(failureLogPath(repoRoot))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parsing %s: %w", failureLogPath(repoRoot), err)
	}
	return rec, nil
}

// captureOutput swaps os.Stdout/os.Stderr for the duration of fn so that
// everything fn writes to either is duplicated into the returned byte
// slice, while still reaching the user's real terminal immediately and
// unmodified. The original streams are restored before returning, even if
// fn panics.
//
// When the real stdout is a terminal, the child is given a pseudo-terminal
// rather than a plain pipe: cargo test, pytest, and jest/vitest all check
// isatty(stdout) to decide whether to emit ANSI color, and a plain pipe -
// even in a fully interactive session - would silently turn that off
// purely because this capture-for-`fastci analyze` mechanism exists.
func captureOutput(fn func() error) ([]byte, error) {
	if runner.IsTerminal(os.Stdout) {
		if buf, err, ok := captureOutputPTY(fn); ok {
			return buf, err
		}
		// PTY allocation failed (e.g. no /dev/ptmx) - fall through to the
		// plain-pipe path rather than losing capture entirely.
	}
	return captureOutputPipe(fn)
}

// captureOutputPTY is captureOutput's terminal-preserving path. ok is false
// if the pty itself couldn't be set up, in which case fn was NOT run yet
// and the caller should fall back to captureOutputPipe.
func captureOutputPTY(fn func() error) (output []byte, runErr error, ok bool) {
	master, slavePath, err := runner.OpenPTY()
	if err != nil {
		return nil, nil, false
	}
	defer master.Close()

	slave, err := os.OpenFile(slavePath, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, false
	}
	// The child's own pty already turns its "\n" into "\r\n" (ONLCR, on by
	// default); relaying that through the real terminal - which applies
	// the same translation again on every write - would double the "\r"
	// on every line. Disabling ONLCR here means the child's "\n" passes
	// through unchanged, so the real terminal's own translation is the
	// only one that ever runs.
	disableONLCR(slave)

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = slave, slave
	var buf bytes.Buffer
	copyDone := make(chan struct{})
	go func() {
		io.Copy(io.MultiWriter(origOut, &buf), master)
		close(copyDone)
	}()

	runErr = func() error {
		defer func() {
			os.Stdout, os.Stderr = origOut, origErr
			slave.Close()
		}()
		return fn()
	}()
	<-copyDone

	return buf.Bytes(), runErr, true
}

// disableONLCR clears the pty's ONLCR output flag (best-effort - a failure
// here just means the double-"\r" cosmetic quirk described above isn't
// worth aborting the capture over).
func disableONLCR(f *os.File) {
	t, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		return
	}
	t.Oflag &^= unix.ONLCR
	_ = unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, t)
}

// captureOutputPipe is captureOutput's fallback path for when stdout isn't
// a terminal (the normal CI case: there's no color/isatty behavior to
// preserve, since the child would see a non-terminal stdout either way).
func captureOutputPipe(fn func() error) ([]byte, error) {
	origOut, origErr := os.Stdout, os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		// Capture is best-effort - if we can't even open a pipe, just run
		// fn normally rather than fail the whole test run over it.
		return nil, fn()
	}

	os.Stdout, os.Stderr = w, w
	var buf bytes.Buffer
	copyDone := make(chan struct{})
	go func() {
		io.Copy(io.MultiWriter(origOut, &buf), r)
		close(copyDone)
	}()

	runErr := func() error {
		defer func() {
			os.Stdout, os.Stderr = origOut, origErr
			w.Close()
		}()
		return fn()
	}()
	<-copyDone

	return buf.Bytes(), runErr
}
