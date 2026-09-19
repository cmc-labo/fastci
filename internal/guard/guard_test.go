package guard_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/guard"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckersDetectTheirOwnEcosystem(t *testing.T) {
	cases := []struct {
		checker guard.Checker
		file    string
	}{
		{guard.GoVulnCheck{}, "go.mod"},
		{guard.JSAudit{}, "package.json"},
		{guard.PipAudit{}, "requirements.txt"},
		{guard.CargoAudit{}, "Cargo.toml"},
	}
	for _, c := range cases {
		t.Run(c.checker.Name(), func(t *testing.T) {
			dir := t.TempDir()
			ok, err := c.checker.Detect(dir)
			if err != nil {
				t.Fatalf("Detect on empty dir: %v", err)
			}
			if ok {
				t.Error("Detect = true on an empty directory, want false")
			}

			writeFile(t, filepath.Join(dir, c.file), "")
			ok, err = c.checker.Detect(dir)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if !ok {
				t.Errorf("Detect = false after creating %s, want true", c.file)
			}
		})
	}
}

func TestJSAuditPicksPackageManagerByLockfile(t *testing.T) {
	cases := []struct {
		lockfile string
		wantBin  string
	}{
		{"pnpm-lock.yaml", "pnpm"},
		{"yarn.lock", "yarn"},
		{"package-lock.json", "npm"},
		{"npm-shrinkwrap.json", "npm"},
	}
	for _, c := range cases {
		t.Run(c.wantBin, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "package.json"), "{}")
			writeFile(t, filepath.Join(dir, c.lockfile), "")
			// BinaryAvailable's install hint mentions the chosen binary
			// name, giving us a black-box way to check the selection
			// without exporting jsAuditCommand.
			_, hint := guard.JSAudit{}.BinaryAvailable(dir)
			if hint != "" && !strings.Contains(hint, c.wantBin) {
				t.Errorf("install hint = %q, want it to mention %q", hint, c.wantBin)
			}
		})
	}
}

// TestJSAuditSkipsWithoutAnyLockfile guards a real bug: "npm audit" (the
// default when no pnpm/yarn lockfile is present) requires a lockfile of
// its own and fails outright without one - "npm error code ENOLOCK ...
// This command requires an existing lockfile" - exiting 1 exactly like it
// would for "vulnerabilities found". Without this BinaryAvailable check, a
// package.json with no committed lockfile at all would make guard falsely
// report a vulnerability that npm audit never actually looked for.
func TestJSAuditSkipsWithoutAnyLockfile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), "{}")

	ok, hint := guard.JSAudit{}.BinaryAvailable(dir)
	if ok {
		t.Fatal("BinaryAvailable = true with no lockfile at all, want false")
	}
	if !strings.Contains(hint, "lockfile") {
		t.Errorf("hint = %q, want it to explain that a lockfile is needed", hint)
	}
}

// TestCheckersListedInFixedOrder guards a small but real UX property: the
// order guard reports checkers in should be stable across runs, not
// dependent on map iteration or similar.
func TestCheckersListedInFixedOrder(t *testing.T) {
	names1 := checkerNames(guard.Checkers())
	names2 := checkerNames(guard.Checkers())
	if len(names1) != 5 {
		t.Fatalf("Checkers() returned %d checkers, want 5", len(names1))
	}
	for i := range names1 {
		if names1[i] != names2[i] {
			t.Errorf("Checkers() order is not stable: %v vs %v", names1, names2)
			break
		}
	}
}

func checkerNames(cs []guard.Checker) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Name()
	}
	return out
}

// TestRunReportsMissingBinaryAsError guards runChecker's own contract
// indirectly: asking a checker to Run when its binary isn't on PATH must
// return an error (an infra problem), not a Result claiming success.
func TestGoVulnCheckRunFailsCleanlyWithoutTheBinary(t *testing.T) {
	gv := guard.GoVulnCheck{}
	if ok, _ := gv.BinaryAvailable(""); ok {
		t.Skip("govulncheck is installed in this environment - nothing to test here")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module m\n\ngo 1.21\n")
	_, err := gv.Run(context.Background(), dir)
	if err == nil {
		t.Error("Run() with no govulncheck binary on PATH should return an error")
	}
}
