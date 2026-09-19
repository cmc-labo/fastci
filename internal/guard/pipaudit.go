package guard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// PipAudit wraps pip-audit (https://pypi.org/project/pip-audit/), the PyPA
// project's own vulnerability scanner. When a requirements.txt is present,
// it's audited directly (no need for the project's own dependencies to be
// installed, matching pytestanalyzer's own "never requires the project's
// deps installed" approach); otherwise pip-audit falls back to its default
// of auditing the currently active Python environment's installed
// packages, which does require them to already be installed.
type PipAudit struct{}

func (PipAudit) Name() string { return "pip-audit" }

func (PipAudit) Detect(dir string) (bool, error) {
	for _, name := range []string{"requirements.txt", "pyproject.toml", "Pipfile", "setup.py", "setup.cfg"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func (PipAudit) BinaryAvailable(dir string) (bool, string) {
	if _, err := exec.LookPath("pip-audit"); err == nil {
		return true, ""
	}
	return false, "pip-audit not found on PATH (install: pip install pip-audit, or: pipx install pip-audit)"
}

func (PipAudit) Run(ctx context.Context, dir string) (Result, error) {
	args := []string{"pip-audit"}
	if _, err := os.Stat(filepath.Join(dir, "requirements.txt")); err == nil {
		args = append(args, "-r", "requirements.txt")
	}
	return runChecker(ctx, "pip-audit", dir, args)
}
