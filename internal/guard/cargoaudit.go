package guard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	return false, "cargo install cargo-audit"
}

func (CargoAudit) Run(ctx context.Context, dir string) (Result, error) {
	return runChecker(ctx, "cargo-audit", dir, []string{"cargo", "audit"})
}
