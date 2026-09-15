package guard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// JSAudit runs the JS package manager's own built-in audit command,
// choosing npm/pnpm/yarn by which lockfile is present in dir - the same
// signal jestanalyzer/vitestanalyzer's own FullRunFile already treats as
// significant, so it stays consistent with how fastci identifies a
// project's package manager elsewhere.
type JSAudit struct{}

func (JSAudit) Name() string { return "js audit" }

func (JSAudit) Detect(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "package.json"))
	return err == nil, nil
}

func (JSAudit) BinaryAvailable(dir string) (bool, string) {
	bin, _ := jsAuditCommand(dir)
	if _, err := exec.LookPath(bin); err == nil {
		return true, ""
	}
	return false, bin + " not found on PATH (install Node.js/" + bin + ")"
}

func (JSAudit) Run(ctx context.Context, dir string) (Result, error) {
	bin, args := jsAuditCommand(dir)
	return runChecker(ctx, "js audit ("+bin+")", dir, append([]string{bin}, args...))
}

// jsAuditCommand picks the package manager (and its audit subcommand
// invocation) based on which lockfile is present in dir, defaulting to npm
// when none is (e.g. a package.json with dependencies pinned only via
// version ranges, no lockfile committed).
func jsAuditCommand(dir string) (bin string, args []string) {
	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		return "pnpm", []string{"audit"}
	}
	if _, err := os.Stat(filepath.Join(dir, "yarn.lock")); err == nil {
		return "yarn", []string{"audit"}
	}
	return "npm", []string{"audit"}
}
