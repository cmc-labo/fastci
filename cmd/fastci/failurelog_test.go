package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/runner"
)

// fakeRunTestsAnalyzer is a minimal analyzer.Analyzer stub whose RunTests
// prints to stdout/stderr and returns a fixed error, for exercising
// runAndRecord/captureOutput without any real test runner.
type fakeRunTestsAnalyzer struct {
	name       string
	stdout     string
	stderr     string
	runErr     error
	callTarget []string
}

func (f *fakeRunTestsAnalyzer) Name() string                            { return f.name }
func (f *fakeRunTestsAnalyzer) Detect(dir string) (bool, error)         { return true, nil }
func (f *fakeRunTestsAnalyzer) Build(dir string) (*graph.Graph, error)  { return graph.New(), nil }
func (f *fakeRunTestsAnalyzer) FullRunFile(absPath string) bool         { return false }
func (f *fakeRunTestsAnalyzer) Ignorable(absPath string) bool           { return true }
func (f *fakeRunTestsAnalyzer) AllTargets(dir string) ([]string, error) { return nil, nil }
func (f *fakeRunTestsAnalyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	f.callTarget = targets
	fmt.Print(f.stdout)
	fmt.Fprint(os.Stderr, f.stderr)
	return f.runErr
}

func TestCaptureOutputForwardsAndCaptures(t *testing.T) {
	forwarded := captureStdout(t, func() {
		captured, err := captureOutput(func() error {
			fmt.Print("hello from inside")
			return nil
		})
		if err != nil {
			t.Fatalf("captureOutput: %v", err)
		}
		if string(captured) != "hello from inside" {
			t.Errorf("captured = %q, want %q", captured, "hello from inside")
		}
	})
	if forwarded != "hello from inside" {
		t.Errorf("output not forwarded to the real stdout: got %q", forwarded)
	}
}

// TestCaptureOutputPreservesTerminalForColorDetection guards against a real
// bug: captureOutput unconditionally used a plain os.Pipe for the child,
// which many test runners (cargo test, pytest, jest/vitest) detect via
// isatty(stdout) to decide whether to emit ANSI color - silently turning
// color off even in a fully interactive session, purely because this
// capture-for-`fastci analyze` mechanism existed. With a real terminal on
// the actual stdout, fn must still see a terminal.
func TestCaptureOutputPreservesTerminalForColorDetection(t *testing.T) {
	master, slavePath, err := runner.OpenPTY()
	if err != nil {
		t.Skipf("no pty support in this environment: %v", err)
	}
	defer master.Close()
	slave, err := os.OpenFile(slavePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer slave.Close()
	go io.Copy(io.Discard, master) // drain so writes to the outer "terminal" never block

	origStdout := os.Stdout
	os.Stdout = slave
	defer func() { os.Stdout = origStdout }()

	var sawTerminal bool
	captured, err := captureOutput(func() error {
		sawTerminal = runner.IsTerminal(os.Stdout)
		fmt.Print("line one\nline two\n")
		return nil
	})
	if err != nil {
		t.Fatalf("captureOutput: %v", err)
	}
	if !sawTerminal {
		t.Error("fn's os.Stdout was not a terminal - captureOutput must preserve TTY-ness when the real stdout is one, or color-detecting test runners silently lose their ANSI output")
	}
	// Also guards the ONLCR fix: relaying an inner pty's own "\n"->"\r\n"
	// translation through the outer terminal's *own* identical translation
	// would otherwise double the "\r" on every line.
	if bytes.Contains(captured, []byte("\r\r\n")) {
		t.Errorf("captured output contains a doubled \\r (%q) - the inner pty's ONLCR must be disabled before relaying through a real terminal", captured)
	}
}

func TestRunAndRecordClearsFailureLogOnSuccess(t *testing.T) {
	repoRoot := t.TempDir()
	// Pre-seed a stale failure log from a hypothetical earlier failed run.
	if err := writeFailureLog(repoRoot, failureLog{Analyzer: "go", Error: "stale"}); err != nil {
		t.Fatal(err)
	}

	a := &fakeRunTestsAnalyzer{name: "go", stdout: "ok\n"}
	_ = captureStdoutErr(t, func() error {
		return runAndRecord(context.Background(), repoRoot, a, repoRoot, []string{"pkg"}, nil)
	})

	if _, err := os.Stat(failureLogPath(repoRoot)); !os.IsNotExist(err) {
		t.Error("a successful run must clear any previous failure log, so `fastci analyze` doesn't describe an already-fixed problem")
	}
}

func TestRunAndRecordWritesFailureLogOnFailure(t *testing.T) {
	repoRoot := t.TempDir()
	wantErr := errors.New("exit status 1")
	a := &fakeRunTestsAnalyzer{name: "pytest", stdout: "FAILED tests/test_x.py\n", stderr: "traceback...\n", runErr: wantErr}

	_ = captureStdoutErr(t, func() error {
		err := runAndRecord(context.Background(), repoRoot, a, repoRoot, []string{"tests/test_x.py"}, []string{"-x"})
		if err != wantErr {
			t.Errorf("runAndRecord error = %v, want %v", err, wantErr)
		}
		return err
	})

	rec, err := readFailureLog(repoRoot)
	if err != nil {
		t.Fatalf("readFailureLog: %v", err)
	}
	if rec.Analyzer != "pytest" {
		t.Errorf("Analyzer = %q, want pytest", rec.Analyzer)
	}
	if rec.Error != wantErr.Error() {
		t.Errorf("Error = %q, want %q", rec.Error, wantErr.Error())
	}
	if !strings.Contains(rec.Output, "FAILED tests/test_x.py") || !strings.Contains(rec.Output, "traceback...") {
		t.Errorf("Output = %q, want it to contain both stdout and stderr content", rec.Output)
	}
	if len(rec.Targets) != 1 || rec.Targets[0] != "tests/test_x.py" {
		t.Errorf("Targets = %v, want [tests/test_x.py]", rec.Targets)
	}
}

func TestRunAndRecordTruncatesLargeOutputKeepingTail(t *testing.T) {
	repoRoot := t.TempDir()
	big := strings.Repeat("x", failureLogMaxOutput+1000) + "TAIL_MARKER"
	a := &fakeRunTestsAnalyzer{name: "go", stdout: big, runErr: errors.New("fail")}

	_ = captureStdoutErr(t, func() error {
		return runAndRecord(context.Background(), repoRoot, a, repoRoot, nil, nil)
	})

	rec, err := readFailureLog(repoRoot)
	if err != nil {
		t.Fatalf("readFailureLog: %v", err)
	}
	if !rec.Truncated {
		t.Error("Truncated = false, want true for output exceeding failureLogMaxOutput")
	}
	if len(rec.Output) > failureLogMaxOutput {
		t.Errorf("len(Output) = %d, want <= %d", len(rec.Output), failureLogMaxOutput)
	}
	if !strings.HasSuffix(rec.Output, "TAIL_MARKER") {
		t.Error("truncation must keep the tail of the output, not the head")
	}
}

// TestTruncateUTF8TailNeverSplitsARune guards against a real bug: a plain
// byte-offset cut (output[len(output)-max:]) can land in the middle of a
// multi-byte UTF-8 rune (e.g. non-ASCII text in a failure message or file
// path), leaving an invalid leading byte sequence that both encoding/json
// and the Anthropic API request silently render as a corrupted "�"
// character right at the truncation point.
func TestTruncateUTF8TailNeverSplitsARune(t *testing.T) {
	base := strings.Repeat("x", 100) + "日本語のテスト失敗メッセージ" + strings.Repeat("y", 100)
	// Sweep every possible cap so at least one lands mid-rune (confirmed by
	// direct byte slicing, exercised below as a sanity check of the test
	// itself), and verify truncateUTF8Tail is valid UTF-8 for all of them.
	sawInvalidNaiveCut := false
	for max := 1; max < len(base); max++ {
		if naive := base[len(base)-max:]; !utf8.ValidString(naive) {
			sawInvalidNaiveCut = true
		}
		got := truncateUTF8Tail([]byte(base), max)
		if !utf8.Valid(got) {
			t.Fatalf("truncateUTF8Tail(base, %d) = %q, not valid UTF-8", max, got)
		}
		if !strings.HasSuffix(base, string(got)) {
			t.Fatalf("truncateUTF8Tail(base, %d) = %q is not a suffix of the input", max, got)
		}
	}
	if !sawInvalidNaiveCut {
		t.Fatal("test setup: no cap value produced an invalid naive byte cut - this test isn't exercising the bug")
	}
}

// TestStripANSI guards against a real bug introduced by making
// captureOutput preserve colors for an interactive run (captureOutputPTY):
// the raw ANSI escape sequences that come along with that now-preserved
// color output would otherwise end up verbatim in .fastci-cache/last-failure.json
// and in the plain-text prompt sent to the Anthropic API by `fastci
// analyze` - illegible \x1b[...m noise there, unlike in a real terminal.
// This uses the exact escape sequences observed from a real `cargo test`
// run captured through a pty (SGR color codes, an OSC 8 hyperlink, and a
// character-set-selection sequence).
func TestStripANSI(t *testing.T) {
	input := "\x1b[1m\x1b[32m   Compiling\x1b[0m demo v0.1.0 (/tmp/demo)\n" +
		"\x1b[1m\x1b[32m    Finished\x1b[0m \x1b]8;;https://doc.rust-lang.org/cargo/reference/profiles.html#default-profiles\x1b\\`test` profile [unoptimized + debuginfo]\x1b]8;;\x1b\\ target(s) in 0.72s\n" +
		"test tests::test_fail ... \x1b[31mFAILED\x1b(B\x1b[m\n" +
		"test result: \x1b[31mFAILED\x1b(B\x1b[m. 1 failed\n"
	want := "   Compiling demo v0.1.0 (/tmp/demo)\n" +
		"    Finished `test` profile [unoptimized + debuginfo] target(s) in 0.72s\n" +
		"test tests::test_fail ... FAILED\n" +
		"test result: FAILED. 1 failed\n"

	got := stripANSI([]byte(input))
	if string(got) != want {
		t.Errorf("stripANSI =\n%q\nwant\n%q", got, want)
	}
}

// TestRunAndRecordStripsANSIFromPersistedOutput is the integration-level
// guard for the same bug: a failing run whose captured output contains
// ANSI codes (as happens whenever captureOutput takes the pty path - see
// TestCaptureOutputPreservesTerminalForColorDetection) must not leak them
// into the persisted failureLog.Output.
func TestRunAndRecordStripsANSIFromPersistedOutput(t *testing.T) {
	repoRoot := t.TempDir()
	a := &fakeRunTestsAnalyzer{
		name:   "cargo",
		stdout: "test tests::test_fail ... \x1b[31mFAILED\x1b[0m\n",
		runErr: errors.New("exit status 101"),
	}

	_ = captureStdoutErr(t, func() error {
		return runAndRecord(context.Background(), repoRoot, a, repoRoot, nil, nil)
	})

	rec, err := readFailureLog(repoRoot)
	if err != nil {
		t.Fatalf("readFailureLog: %v", err)
	}
	if strings.ContainsRune(rec.Output, 0x1b) {
		t.Errorf("Output = %q still contains a raw ESC byte - it must be stripped before persisting", rec.Output)
	}
	if !strings.Contains(rec.Output, "test tests::test_fail ... FAILED") {
		t.Errorf("Output = %q, want the de-colored text to still be present", rec.Output)
	}
}

func TestFailureLogPathIsGitignored(t *testing.T) {
	repoRoot := t.TempDir()
	if err := writeFailureLog(repoRoot, failureLog{Analyzer: "go"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(repoRoot + "/.fastci-cache/.gitignore")
	if err != nil {
		t.Fatalf(".fastci-cache/.gitignore was not created: %v", err)
	}
	if string(data) != "*\n" {
		t.Errorf(".gitignore content = %q, want \"*\\n\"", data)
	}
}

// captureStdoutErr is like captureStdout but for a function that returns an
// error, propagating it to the caller instead of failing the test.
func captureStdoutErr(t *testing.T, fn func() error) error {
	t.Helper()
	var err error
	captureStdout(t, func() {
		err = fn()
	})
	return err
}
