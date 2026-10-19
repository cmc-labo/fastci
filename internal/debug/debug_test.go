package debug_test

import (
	"os"
	"strings"
	"testing"

	"github.com/cmc-labo/fastci/internal/debug"
)

// captureStderr redirects os.Stderr for the duration of fn and returns
// everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	w.Close()
	var buf strings.Builder
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

func TestLogfSilentByDefault(t *testing.T) {
	t.Setenv("FASTCI_DEBUG", "")
	out := captureStderr(t, func() {
		debug.Logf("should not appear")
	})
	if out != "" {
		t.Errorf("Logf wrote %q with FASTCI_DEBUG unset, want nothing", out)
	}
}

func TestLogfPrintsWhenEnabled(t *testing.T) {
	t.Setenv("FASTCI_DEBUG", "1")
	out := captureStderr(t, func() {
		debug.Logf("hello %s", "world")
	})
	if !strings.Contains(out, "hello world") {
		t.Errorf("Logf output = %q, want it to contain %q", out, "hello world")
	}
	if !strings.HasPrefix(out, "fastci: [debug] ") {
		t.Errorf("Logf output = %q, want the fastci: [debug] prefix", out)
	}
}

func TestEnabled(t *testing.T) {
	t.Setenv("FASTCI_DEBUG", "")
	if debug.Enabled() {
		t.Error("Enabled() = true with FASTCI_DEBUG unset, want false")
	}
	t.Setenv("FASTCI_DEBUG", "1")
	if !debug.Enabled() {
		t.Error("Enabled() = false with FASTCI_DEBUG set, want true")
	}
}

func TestCommandNoopWhenDisabled(t *testing.T) {
	t.Setenv("FASTCI_DEBUG", "")
	out := captureStderr(t, func() {
		done := debug.Command("test", []string{"echo", "hi"}, "/tmp")
		done()
	})
	if out != "" {
		t.Errorf("Command wrote %q with FASTCI_DEBUG unset, want nothing", out)
	}
}

func TestCommandLogsStartAndFinishWhenEnabled(t *testing.T) {
	t.Setenv("FASTCI_DEBUG", "1")
	out := captureStderr(t, func() {
		done := debug.Command("test", []string{"echo", "hi"}, "/tmp")
		done()
	})
	if !strings.Contains(out, "running") || !strings.Contains(out, "echo hi") {
		t.Errorf("Command output = %q, want it to mention the command running", out)
	}
	if !strings.Contains(out, "finished") {
		t.Errorf("Command output = %q, want it to mention finishing (from done())", out)
	}
}
