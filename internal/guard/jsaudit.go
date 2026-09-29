package guard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	name := "js audit (" + bin + ")"
	output, exitCode, err := runCmd(ctx, name, dir, append([]string{bin}, args...), nil)
	if err != nil {
		return Result{}, err
	}
	// npm/pnpm/yarn audit all reuse the very same exit code both for a
	// real finding and for failing to even reach the registry's audit
	// endpoint - distinguishable only by their own error text, never by
	// exit code alone. Each of the three's own specific text (npm's
	// "audit endpoint returned an error", pnpm's "ERR_PNPM_AUDIT_BAD_
	// RESPONSE") is verified directly against the real tool; the Node.js
	// network error codes (ECONNREFUSED et al.) and the plain-English
	// "Connection refused"/"Connection reset" phrasing (pnpm's own Rust
	// HTTP client prints the OS errno's description, not a Node-style
	// error code) are a broader net for whatever exact wording any of the
	// three uses for the same underlying failure in a slightly different
	// version.
	if exitCode != 0 && looksLikeJSAuditInfraFailure(output) {
		return Result{}, fmt.Errorf("%s: exited %d without completing the audit (looks like a network problem reaching the registry, not a vulnerability finding):\n%s", name, exitCode, output)
	}
	return Result{CheckerName: name, Output: output, FoundIssues: exitCode != 0}, nil
}

func looksLikeJSAuditInfraFailure(output string) bool {
	markers := []string{
		"audit endpoint returned an error",                     // npm
		"ERR_PNPM_AUDIT_BAD_RESPONSE",                          // pnpm
		"ECONNREFUSED", "ENOTFOUND", "ETIMEDOUT", "ECONNRESET", // Node.js network error codes
		"Connection refused", "Connection reset", "Connection timed out", // OS errno text (e.g. pnpm's Rust HTTP client)
	}
	for _, m := range markers {
		if strings.Contains(output, m) {
			return true
		}
	}
	return false
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
