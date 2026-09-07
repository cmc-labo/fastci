package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/graph"
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
