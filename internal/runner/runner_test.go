package runner_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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

// openPTY opens a fresh pseudo-terminal and returns its master fd and the
// path to the corresponding slave device.
func openPTY(t *testing.T) (master *os.File, slavePath string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { m.Close() })

	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock pty: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("get pty number: %v", err)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n)
}

// TestRunSkipsProcessGroupIsolationWithControllingTerminal guards against a
// regression from the process-group isolation fix above: with a real
// controlling terminal on stdin, Run must NOT put the child in its own
// process group. A background process group that tries to read from the
// controlling terminal is stopped by SIGTTIN (hit by, e.g., a pytest test
// that drops into a debugger breakpoint) - this was reproduced manually
// (a pty-attached fastci run hung indefinitely once the child tried to
// read stdin) before this guard was added.
func TestRunSkipsProcessGroupIsolationWithControllingTerminal(t *testing.T) {
	_, slavePath := openPTY(t)
	slave, err := os.OpenFile(slavePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open pty slave: %v", err)
	}
	defer slave.Close()

	orig := os.Stdin
	os.Stdin = slave
	defer func() { os.Stdin = orig }()

	ownPgid, err := unix.Getpgid(0)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}

	dir := t.TempDir()
	outFile := filepath.Join(dir, "pgid.txt")
	script := "ps -o pgid= -p $$ | tr -d ' ' > " + outFile

	if err := runner.Run(context.Background(), runner.Options{Dir: dir, Argv: []string{"sh", "-c", script}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading child's reported pgid: %v", err)
	}
	childPgid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parsing child's reported pgid %q: %v", data, err)
	}

	if childPgid != ownPgid {
		t.Errorf("child's process group = %d, want %d (this test's own group) - it must not be isolated into a new group while a controlling terminal is attached", childPgid, ownPgid)
	}
}
