package guard_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hpscript/fastci/internal/guard"
)

// deadProxyEnv points HTTP_PROXY/HTTPS_PROXY at a port nothing listens on,
// forcing any outbound HTTP(S) request a checker's underlying tool makes
// to fail immediately with a real connection-refused error - the same
// technique used to reproduce each of these bugs manually before fixing
// them (see each checker's own comments for the exact tool behavior this
// guards against).
func deadProxyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
}

// TestGoVulnCheckNetworkFailureIsNotFoundIssues reproduces a real bug:
// govulncheck exits non-zero (documented as exit 1) when it can't fetch
// its vulnerability database over the network - a different, specific
// exit code (3) means an actual finding. Treating any non-zero exit as
// FoundIssues would misreport a network hiccup as a real vulnerability.
func TestGoVulnCheckNetworkFailureIsNotFoundIssues(t *testing.T) {
	if _, err := exec.LookPath("govulncheck"); err != nil {
		t.Skip("govulncheck not installed - skipping this real integration test")
	}
	deadProxyEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := guard.GoVulnCheck{}.Run(ctx, dir)
	if err == nil {
		t.Fatalf("Run: want an error for a network failure, got a clean Result: %+v", res)
	}
}

// TestCargoAuditNetworkFailureIsNotFoundIssues reproduces the same class
// of bug for cargo-audit, which reuses exit code 1 for both a real
// finding and a failure to fetch its advisory database - distinguishable
// only by its own error message.
func TestCargoAuditNetworkFailureIsNotFoundIssues(t *testing.T) {
	if _, err := exec.LookPath("cargo-audit"); err != nil {
		t.Skip("cargo-audit not installed - skipping this real integration test")
	}
	deadProxyEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"m\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	writeFile(t, filepath.Join(dir, "src", "main.rs"), "fn main() {}\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := guard.CargoAudit{}.Run(ctx, dir)
	if err == nil {
		t.Fatalf("Run: want an error for a network failure, got a clean Result: %+v", res)
	}
}

// TestPipAuditNetworkFailureIsNotFoundIssues reproduces the same class of
// bug for pip-audit.
func TestPipAuditNetworkFailureIsNotFoundIssues(t *testing.T) {
	if _, err := exec.LookPath("pip-audit"); err != nil {
		t.Skip("pip-audit not installed - skipping this real integration test")
	}
	deadProxyEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "requirements.txt"), "requests==2.25.1\n")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := guard.PipAudit{}.Run(ctx, dir)
	if err == nil {
		t.Fatalf("Run: want an error for a network failure, got a clean Result: %+v", res)
	}
}

// TestJSAuditNetworkFailureIsNotFoundIssues reproduces the same class of
// bug for npm audit.
func TestJSAuditNetworkFailureIsNotFoundIssues(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed - skipping this real integration test")
	}
	deadProxyEnv(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"m","version":"1.0.0","dependencies":{"left-pad":"1.3.0"}}`)
	writeFile(t, filepath.Join(dir, "package-lock.json"), `{
  "name": "m",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "m",
      "version": "1.0.0",
      "dependencies": {
        "left-pad": "1.3.0"
      }
    },
    "node_modules/left-pad": {
      "version": "1.3.0"
    }
  }
}`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := guard.JSAudit{}.Run(ctx, dir)
	if err == nil {
		t.Fatalf("Run: want an error for a network failure, got a clean Result: %+v", res)
	}
}
