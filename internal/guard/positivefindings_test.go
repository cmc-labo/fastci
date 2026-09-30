package guard_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hpscript/fastci/internal/guard"
)

// These tests are the mirror image of networkfailure_test.go: they check
// that a checker whose underlying tool completes its scan and finds a real,
// known vulnerability still correctly reports FoundIssues, and is not
// accidentally swallowed by this project's own infra-failure detection
// (see each checker's looksLike*InfraFailure/classify* function). Without
// them, a classifier that matched real finding output as well as real
// failure output would pass every test in networkfailure_test.go while
// silently breaking guard's actual purpose.
//
// Each uses a real, currently-known-vulnerable dependency pin, verified
// directly against the real tool while writing these tests: lodash@4.17.15
// (npm/pnpm/yarn, several GHSA advisories including a high-severity Command
// Injection), time@0.1.45 (cargo-audit, RUSTSEC-2020-0071), and
// golang.org/x/text@v0.3.0 with a call into language.Parse (govulncheck,
// GO-2021-0113 - the reachability analysis requires an actual call site,
// not just the dependency being present).

func TestJSAuditNpmFindsRealVulnerability(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not installed - skipping this real integration test")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"m","version":"1.0.0","dependencies":{"lodash":"4.17.15"}}`)
	run(t, dir, "npm", "install", "--package-lock-only")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := guard.JSAudit{}.Run(ctx, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true (lodash@4.17.15 has known vulnerabilities); output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "lodash") {
		t.Errorf("output doesn't mention \"lodash\"; output:\n%s", res.Output)
	}
}

func TestJSAuditPnpmFindsRealVulnerability(t *testing.T) {
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Skip("pnpm not installed - skipping this real integration test")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"m","version":"1.0.0","dependencies":{"lodash":"4.17.15"}}`)
	run(t, dir, "pnpm", "install")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := guard.JSAudit{}.Run(ctx, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true (lodash@4.17.15 has known vulnerabilities); output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "lodash") {
		t.Errorf("output doesn't mention \"lodash\"; output:\n%s", res.Output)
	}
}

func TestJSAuditYarnFindsRealVulnerability(t *testing.T) {
	if _, err := exec.LookPath("yarn"); err != nil {
		t.Skip("yarn not installed - skipping this real integration test")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"m","version":"1.0.0","dependencies":{"lodash":"4.17.15"}}`)
	run(t, dir, "yarn", "install")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := guard.JSAudit{}.Run(ctx, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true (lodash@4.17.15 has known vulnerabilities); output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "lodash") {
		t.Errorf("output doesn't mention \"lodash\"; output:\n%s", res.Output)
	}
}

func TestCargoAuditFindsRealVulnerability(t *testing.T) {
	if _, err := exec.LookPath("cargo-audit"); err != nil {
		t.Skip("cargo-audit not installed - skipping this real integration test")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"m\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[dependencies]\ntime = \"0.1.45\"\n")
	writeFile(t, filepath.Join(dir, "src", "main.rs"), "fn main() {}\n")
	run(t, dir, "cargo", "generate-lockfile")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := guard.CargoAudit{}.Run(ctx, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true (time@0.1.45 is RUSTSEC-2020-0071); output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "RUSTSEC-2020-0071") {
		t.Errorf("output doesn't mention \"RUSTSEC-2020-0071\"; output:\n%s", res.Output)
	}
}

func TestGoVulnCheckFindsRealVulnerability(t *testing.T) {
	if _, err := exec.LookPath("govulncheck"); err != nil {
		t.Skip("govulncheck not installed - skipping this real integration test")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n\nrequire golang.org/x/text v0.3.0\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nimport \"golang.org/x/text/language\"\n\nfunc main() {\n\t_, _ = language.Parse(\"en\")\n}\n")
	run(t, dir, "go", "mod", "tidy")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := guard.GoVulnCheck{}.Run(ctx, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true (golang.org/x/text@v0.3.0's language.Parse is GO-2021-0113, called directly); output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "GO-2021-0113") {
		t.Errorf("output doesn't mention \"GO-2021-0113\"; output:\n%s", res.Output)
	}
}
