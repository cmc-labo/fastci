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
// deps installed" approach); otherwise pip-audit falls back to auditing
// the currently active Python environment's installed packages, which
// does require them to already be installed.
//
// That fallback needs PIPAPI_PYTHON_LOCATION pointed at whatever "python3"
// resolves to on PATH (see Run) - without it, pip-audit resolves packages
// against the interpreter *it itself* happens to be installed under
// (sys.executable), not the caller's active virtualenv. A pip-audit
// installed globally or via pipx would then silently audit the wrong
// environment entirely - anywhere from reporting unrelated vulnerabilities
// to, worse, missing the project's real ones - and pip-audit even prints a
// warning about exactly this ("This may result in unintuitive audits")
// rather than failing loudly, so it's easy to miss.
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
		return runChecker(ctx, "pip-audit", dir, args)
	}

	// No requirements.txt - falling back to auditing the active
	// environment (see the type doc comment for why PIPAPI_PYTHON_LOCATION
	// is required for that to actually target the right one). If "python3"
	// isn't found at all, fall through and let pip-audit itself produce
	// whatever error it normally would with no interpreter to guess from.
	var extraEnv []string
	if python, err := exec.LookPath("python3"); err == nil {
		extraEnv = []string{"PIPAPI_PYTHON_LOCATION=" + python}
	}
	return runCheckerEnv(ctx, "pip-audit", dir, args, extraEnv)
}
