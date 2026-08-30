package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hpscript/fastci/internal/runner"
)

// TestRunKillsWholeProcessGroupOnCancel guards against a real bug: none of
// the test runners fastci drives are leaf processes (go test execs a
// separate test binary, jest/vitest fork worker processes, ...), so
// cancelling the immediate child alone - os/exec's default behavior - left
// its own children running as orphans when fastci itself was terminated
// (e.g. a CI job cancellation) without a controlling terminal's job control
// to clean them up. Run must put the command in its own process group and
// signal the whole group on cancellation.
func TestRunKillsWholeProcessGroupOnCancel(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// Mirrors a real runner: a shell that forks a grandchild (sleep) and
	// then waits on it, so the grandchild is never Run's direct child.
	script := "sleep 30 & echo $! > " + pidFile + "; wait"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx, runner.Options{Dir: dir, Argv: []string{"sh", "-c", script}})
	}()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			if p, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && p > 0 {
				pid = p
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("grandchild pid file never appeared")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within the cancellation grace period")
	}

	// The grandchild is reparented (to init) once its parent shell exits,
	// and reaping it isn't instantaneous - poll briefly rather than racing
	// a single check right after Run returns.
	alive := true
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if err := syscall.Kill(pid, 0); err != nil {
			alive = false
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if alive {
		syscall.Kill(pid, syscall.SIGKILL) // don't leak it past this test
		t.Errorf("grandchild process (pid %d) is still running after cancellation - the whole process group should have been signaled", pid)
	}
}

func TestRunEmptyArgv(t *testing.T) {
	if err := runner.Run(context.Background(), runner.Options{}); err == nil {
		t.Error("Run with empty Argv should return an error")
	}
}
