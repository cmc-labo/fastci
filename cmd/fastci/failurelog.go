package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hpscript/fastci/internal/analyzer"
)

// failureLogMaxOutput caps how much of a run's combined stdout/stderr gets
// persisted (and later sent to the Anthropic API by `fastci analyze`): the
// most relevant part of a test failure - the actual assertion/traceback -
// is almost always near the end, so the *tail* is kept when output is
// larger than this.
const failureLogMaxOutput = 50_000

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

	truncated := false
	if len(output) > failureLogMaxOutput {
		output = output[len(output)-failureLogMaxOutput:]
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
func captureOutput(fn func() error) ([]byte, error) {
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
