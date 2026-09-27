package guard

import (
	"context"
	"fmt"
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

// govulncheckVulnerabilitiesFound is the exit code govulncheck's own docs
// reserve specifically for "your code is affected by a vulnerability it
// can actually reach" - distinct from exit 0 (clean, or only unreachable
// informational findings) and any other non-zero code (a usage error, or -
// in practice, most commonly - a failure to fetch its vulnerability
// database, e.g. over a flaky or firewalled network connection). Treating
// every non-zero exit as a finding, as a naive Unix-tool convention would,
// would misreport a plain network hiccup as a real vulnerability.
const govulncheckVulnerabilitiesFound = 3

func (GoVulnCheck) Run(ctx context.Context, dir string) (Result, error) {
	output, exitCode, err := runCmd(ctx, "govulncheck", dir, []string{"govulncheck", "./..."}, nil)
	if err != nil {
		return Result{}, err
	}
	switch exitCode {
	case 0:
		return Result{CheckerName: "govulncheck", Output: output}, nil
	case govulncheckVulnerabilitiesFound:
		return Result{CheckerName: "govulncheck", Output: output, FoundIssues: true}, nil
	default:
		return Result{}, fmt.Errorf("govulncheck: exited %d without completing the scan (a usage error, or most commonly a failure to fetch its vulnerability database - not a vulnerability finding, which uses exit code %d specifically):\n%s", exitCode, govulncheckVulnerabilitiesFound, output)
	}
}
