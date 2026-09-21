package guard_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hpscript/fastci/internal/guard"
)

// TestPipAuditTargetsActiveVenvNotItsOwnInterpreter is a real integration
// test (network access to PyPI/OSV required, and pip-audit + python3 must
// be installed - it skips cleanly otherwise) reproducing a genuine bug: by
// default, pip-audit's "no requirements.txt -> audit the active
// environment" fallback resolves packages against whatever Python
// interpreter pip-audit *itself* is installed under (sys.executable), not
// necessarily the caller's active virtualenv - pip-audit even warns about
// this ("This may result in unintuitive audits") rather than failing.
//
// A pip-audit installed globally or via pipx therefore silently audited a
// completely unrelated Python environment (fastci's own, or whatever else
// happened to own that interpreter) instead of the project being scanned -
// potentially reporting nothing relevant, or taking minutes auditing a
// large unrelated site-packages, instead of quickly and correctly
// reporting the project's own vulnerable dependency.
//
// This builds a throwaway venv with a single known-vulnerable pin
// (requests==2.25.1, CVE-2023-32681 among others) and checks that
// PipAudit.Run's real output mentions it - which only happens if
// PIPAPI_PYTHON_LOCATION correctly steered pip-audit at that venv's
// python3, not pip-audit's own.
func TestPipAuditTargetsActiveVenvNotItsOwnInterpreter(t *testing.T) {
	if _, err := exec.LookPath("pip-audit"); err != nil {
		t.Skip("pip-audit not installed - skipping this real integration test")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed - skipping this real integration test")
	}

	dir := t.TempDir()
	venv := filepath.Join(dir, "venv")
	run(t, dir, "python3", "-m", "venv", venv)

	venvPython := filepath.Join(venv, "bin", "python3")
	run(t, dir, venvPython, "-m", "pip", "install", "--quiet", "requests==2.25.1")

	// Simulate the venv being "active" the same way a real shell's `source
	// venv/bin/activate` does: prepend its bin/ to PATH, so "python3"
	// resolves to the venv's interpreter - which is exactly the signal
	// PipAudit.Run is supposed to pick up via exec.LookPath("python3").
	t.Setenv("PATH", filepath.Join(venv, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := guard.PipAudit{}.Run(ctx, dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true (requests==2.25.1 has known vulnerabilities); output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "requests") {
		t.Errorf("output doesn't mention the vulnerable package \"requests\" - pip-audit likely audited the wrong (non-venv) environment; output:\n%s", res.Output)
	}
	// The decisive check: this venv contains only requests and its own
	// small set of dependencies (certifi, chardet, idna, urllib3, plus
	// pip/setuptools) - nothing named "aiohttp" was ever installed into
	// it. If pip-audit fell back to auditing the wrong (much larger, e.g.
	// pip-audit's own global) environment instead, "requests" alone isn't
	// a reliable enough signal to catch that - a large unrelated
	// environment can easily happen to *also* contain some vulnerable
	// "requests" version, which is exactly the false pass this project hit
	// while first writing this test. "aiohttp" appearing at all is
	// conclusive proof the wrong environment was audited.
	if strings.Contains(res.Output, "aiohttp") {
		t.Errorf("output mentions \"aiohttp\", which was never installed in this venv - pip-audit audited the wrong (non-venv) environment; output:\n%s", res.Output)
	}
}

func run(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", argv, err, out)
	}
}
