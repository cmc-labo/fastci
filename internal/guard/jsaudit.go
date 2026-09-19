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
	// npm/pnpm/yarn audit all resolve the dependency tree from the
	// lockfile, not package.json's version ranges, so with no lockfile at
	// all "npm audit" (the default when neither pnpm-lock.yaml nor
	// yarn.lock is present) doesn't just find nothing - it fails outright
	// ("npm error code ENOLOCK ... This command requires an existing
	// lockfile", exit code 1). runChecker can't tell that apart from "the
	// tool ran and found vulnerabilities" (both are just a non-zero exit),
	// so without this check a package.json with no committed lockfile
	// would be reported as guard finding a vulnerability it never actually
	// looked for.
	if !hasJSLockfile(dir) {
		return false, `no lockfile found (package-lock.json, npm-shrinkwrap.json, pnpm-lock.yaml, or yarn.lock) - run "npm install" (or pnpm/yarn install) first so there's a lockfile to audit`
	}
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

func hasJSLockfile(dir string) bool {
	for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	return false
}

// jsAuditCommand picks the package manager (and its audit subcommand
// invocation) based on which lockfile is present in dir, defaulting to npm
// when neither pnpm's nor yarn's is (a package-lock.json/npm-shrinkwrap.json,
// or no lockfile at all - the latter is already ruled out by
// BinaryAvailable's hasJSLockfile check before Run is ever called).
func jsAuditCommand(dir string) (bin string, args []string) {
	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		return "pnpm", []string{"audit"}
	}
	if _, err := os.Stat(filepath.Join(dir, "yarn.lock")); err == nil {
		return "yarn", []string{"audit"}
	}
	return "npm", []string{"audit"}
}
