package guard

import "testing"

// This file unit-tests each checker's own network-failure/real-finding
// classifier function directly, using real output captured by actually
// running each tool (against a dead proxy for the failure samples, and
// against genuinely vulnerable fixtures - lodash@4.17.15, requests==2.25.1,
// time@0.1.45, golang.org/x/text@v0.3.0 - for the finding samples). Unlike
// networkfailure_test.go's integration tests, these need no external tool
// installed and run in milliseconds, so they exercise this logic even in
// an environment with none of npm/pnpm/yarn/pip-audit/cargo-audit/
// govulncheck on PATH.

const npmFailureOutput = "npm warn audit request to https://registry.npmjs.org/-/npm/v1/security/audits/quick failed, reason: connect ECONNREFUSED 127.0.0.1:1\n" +
	"undefined\n" +
	"npm error audit endpoint returned an error\n" +
	"npm error A complete log of this run can be found in: /home/vagrant/.npm/_logs/2026-09-29T23_50_46_938Z-debug-0.log\n"

const npmFindingOutput = "# npm audit report\n" +
	"\n" +
	"lodash  <=4.17.23\n" +
	"Severity: high\n" +
	"Command Injection in lodash - https://github.com/advisories/GHSA-35jh-r3h4-6jhm\n" +
	"Prototype Pollution in lodash - https://github.com/advisories/GHSA-p6mc-m468-83gw\n" +
	"Regular Expression Denial of Service (ReDoS) in lodash - https://github.com/advisories/GHSA-29mw-wpgm-hmr9\n" +
	"node_modules/lodash\n" +
	"\n" +
	"1 high severity vulnerability\n"

const pnpmFailureOutput = "Error: ERR_PNPM_AUDIT_BAD_RESPONSE\n" +
	"\n" +
	"  × Failed to request the audit endpoint (at https://registry.npmjs.org/-/npm/\n" +
	"  │ v1/security/advisories/bulk): error sending request for url (https://\n" +
	"  │ registry.npmjs.org/-/npm/v1/security/advisories/bulk)\n" +
	"  ├─▶ client error (Connect)\n" +
	"  ├─▶ tunnel error: failed to create underlying connection\n" +
	"  ├─▶ tcp connect error\n" +
	"  ╰─▶ Connection refused (os error 111)\n"

const pnpmFindingOutput = "┌─────────────────────┬───────────────────────────────────────────────────┐\n" +
	"│ high                │ Command Injection in lodash                       │\n" +
	"├─────────────────────┼───────────────────────────────────────────────────┤\n" +
	"│ Package             │ lodash                                            │\n" +
	"├─────────────────────┼───────────────────────────────────────────────────┤\n" +
	"│ Vulnerable versions │ <4.17.21                                          │\n" +
	"├─────────────────────┼───────────────────────────────────────────────────┤\n" +
	"│ Patched versions    │ >=4.17.21                                         │\n" +
	"└─────────────────────┴───────────────────────────────────────────────────┘\n" +
	"6 vulnerabilities found\n" +
	"Severity: 3 moderate | 3 high\n"

const yarnFailureOutput = "yarn audit v1.22.22\n" +
	"warning package.json: No license field\n" +
	"warning m@1.0.0: No license field\n" +
	"error Error: https://registry.yarnpkg.com/-/npm/v1/security/audits: tunneling socket could not be established, cause=connect ECONNREFUSED 127.0.0.1:1\n" +
	"info Visit https://yarnpkg.com/en/docs/cli/audit for documentation about this command.\n"

const yarnFindingOutput = "yarn audit v1.22.22\n" +
	"warning package.json: No license field\n" +
	"warning m@1.0.0: No license field\n" +
	"┌───────────────┬──────────────────────────────────────────────────────────────┐\n" +
	"│ high          │ Command Injection in lodash                                  │\n" +
	"├───────────────┼──────────────────────────────────────────────────────────────┤\n" +
	"│ Package       │ lodash                                                       │\n" +
	"├───────────────┼──────────────────────────────────────────────────────────────┤\n" +
	"│ Patched in    │ >=4.17.21                                                    │\n" +
	"└───────────────┴──────────────────────────────────────────────────────────────┘\n" +
	"6 vulnerabilities found - Packages audited: 1\n" +
	"Severity: 3 Moderate | 3 High\n"

const pipFailureOutput = "ERROR:pip_audit._cli:Failed to upgrade `pip`: ['/tmp/tmp6z94k_hn/bin/python3', '-m', 'pip', 'install', '--upgrade', 'pip', 'wheel', 'setuptools']\n"

const pipFindingOutput = "Found 22 known vulnerabilities in 3 packages\n" +
	"Name     Version ID              Fix Versions\n" +
	"-------- ------- --------------- ------------\n" +
	"requests 2.25.1  PYSEC-2023-74   2.31.0\n" +
	"idna     2.10    PYSEC-2024-60   3.7\n" +
	"urllib3  1.26.20 PYSEC-2026-1999 2.5.0\n"

const cargoFailureOutput = "    Fetching advisory database from `https://github.com/RustSec/advisory-db.git`\n" +
	"error: couldn't fetch advisory database: git operation failed: failed to prepare fetch\n" +
	"Caused by:\n" +
	"  -> An IO error occurred when talking to the server\n" +
	"  -> error sending request for url (https://github.com/RustSec/advisory-db.git/info/refs?service=git-upload-pack)\n"

const cargoFindingOutput = "    Fetching advisory database from `https://github.com/RustSec/advisory-db.git`\n" +
	"      Loaded 1277 security advisories (from /home/vagrant/.cargo/advisory-db)\n" +
	"    Scanning Cargo.lock for vulnerabilities (7 crate dependencies)\n" +
	"Crate:     time\n" +
	"Version:   0.1.45\n" +
	"Title:     Potential segfault in the time crate\n" +
	"Date:      2020-11-18\n" +
	"ID:        RUSTSEC-2020-0071\n" +
	"URL:       https://rustsec.org/advisories/RUSTSEC-2020-0071\n" +
	"Severity:  6.2 (medium)\n" +
	"Solution:  Upgrade to >=0.2.23\n" +
	"\n" +
	"error: 1 vulnerability found!\n"

func TestLooksLikeJSAuditInfraFailure(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"npm real network failure", npmFailureOutput, true},
		{"npm real vulnerability finding", npmFindingOutput, false},
		{"pnpm real network failure", pnpmFailureOutput, true},
		{"pnpm real vulnerability finding", pnpmFindingOutput, false},
		{"yarn real network failure", yarnFailureOutput, true},
		{"yarn real vulnerability finding", yarnFindingOutput, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeJSAuditInfraFailure(tt.output); got != tt.want {
				t.Errorf("looksLikeJSAuditInfraFailure(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestLooksLikePipAuditInfraFailure(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"pip-audit real network failure", pipFailureOutput, true},
		{"pip-audit real vulnerability finding", pipFindingOutput, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikePipAuditInfraFailure(tt.output); got != tt.want {
				t.Errorf("looksLikePipAuditInfraFailure(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestLooksLikeCargoAuditInfraFailure(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"cargo-audit real network failure", cargoFailureOutput, true},
		{"cargo-audit real vulnerability finding", cargoFindingOutput, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeCargoAuditInfraFailure(tt.output); got != tt.want {
				t.Errorf("looksLikeCargoAuditInfraFailure(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestClassifyGoVulnCheckExit(t *testing.T) {
	tests := []struct {
		name               string
		exitCode           int
		wantFoundIssues    bool
		wantIsInfraFailure bool
	}{
		{"exit 0: clean, real behavior when nothing found", 0, false, false},
		{"exit 3: govulncheck's own documented code for a real finding", govulncheckVulnerabilitiesFound, true, false},
		{"exit 1: real behavior on a network/database-fetch failure", 1, false, true},
		{"exit 2: an unrecognized code is treated as an infra failure, not a finding", 2, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			foundIssues, isInfraFailure := classifyGoVulnCheckExit(tt.exitCode)
			if foundIssues != tt.wantFoundIssues || isInfraFailure != tt.wantIsInfraFailure {
				t.Errorf("classifyGoVulnCheckExit(%d) = (%v, %v), want (%v, %v)",
					tt.exitCode, foundIssues, isInfraFailure, tt.wantFoundIssues, tt.wantIsInfraFailure)
			}
		})
	}
}
