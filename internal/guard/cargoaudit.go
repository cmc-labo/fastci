package guard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CargoAudit wraps cargo-audit (https://github.com/rustsec/rustsec), the
// RustSec project's official advisory-database scanner, invoked as the
// `cargo audit` subcommand.
type CargoAudit struct{}

func (CargoAudit) Name() string { return "cargo-audit" }

func (CargoAudit) Detect(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "Cargo.toml"))
	return err == nil, nil
}

func (CargoAudit) BinaryAvailable(dir string) (bool, string) {
	// cargo-audit installs as a `cargo-audit` binary that `cargo audit`
	// dispatches to as a subcommand - check for the binary directly rather
	// than trying to run `cargo audit` and inspecting its error text.
	if _, err := exec.LookPath("cargo-audit"); err == nil {
		return true, ""
	}
	return false, "cargo-audit not found on PATH (install: cargo install cargo-audit)"
}

func (CargoAudit) Run(ctx context.Context, dir string) (Result, error) {
	output, exitCode, err := runCmd(ctx, "cargo-audit", dir, []string{"cargo", "audit"}, nil)
	if err != nil {
		return Result{}, err
	}
	// cargo-audit reuses the same exit code (1) both for a real finding
	// and for failing to even update its advisory database first (e.g.
	// over a flaky or firewalled network connection) - distinguishable
	// only by its own, specific error message, never by exit code alone.
	if exitCode != 0 && strings.Contains(output, "couldn't fetch advisory database") {
		return Result{}, fmt.Errorf("cargo-audit: exited %d without completing the scan (failed to fetch the advisory database - not a vulnerability finding):\n%s", exitCode, output)
	}
	return Result{CheckerName: "cargo-audit", Output: output, FoundIssues: exitCode != 0}, nil
}
