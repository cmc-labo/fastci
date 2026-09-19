package guard

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// GoVulnCheck wraps the official Go vulnerability scanner, govulncheck
// (golang.org/x/vuln/cmd/govulncheck) - unlike a naive dependency-list
// scan, it also does reachability analysis, only reporting vulnerabilities
// in code paths actually called from the module.
type GoVulnCheck struct{}

func (GoVulnCheck) Name() string { return "govulncheck" }

func (GoVulnCheck) Detect(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil, nil
}

func (GoVulnCheck) BinaryAvailable(dir string) (bool, string) {
	if _, err := exec.LookPath("govulncheck"); err == nil {
		return true, ""
	}
	return false, "govulncheck not found on PATH (install: go install golang.org/x/vuln/cmd/govulncheck@latest)"
}

func (GoVulnCheck) Run(ctx context.Context, dir string) (Result, error) {
	return runChecker(ctx, "govulncheck", dir, []string{"govulncheck", "./..."})
}
