package main

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRunGuardNoRecognizedProjectType is deliberately the one guard test
// that doesn't depend on any of the underlying scanners (govulncheck,
// npm/pnpm/yarn, pip-audit, cargo-audit) actually being installed in the
// environment running `go test` - it only exercises the "nothing detected"
// path, which is self-contained.
func TestRunGuardNoRecognizedProjectType(t *testing.T) {
	dir := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWD)

	out := captureStdout(t, func() {
		if err := runGuard(&cobra.Command{}); err != nil {
			t.Fatalf("runGuard: %v", err)
		}
	})
	if !strings.Contains(out, "nothing to scan") {
		t.Errorf("output = %q, want it to say there's nothing to scan", out)
	}
}
