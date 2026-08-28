// Package pytestanalyzer implements the fastci analyzer.Analyzer interface
// for Python projects tested with pytest, at file granularity (like Jest,
// unlike Go's package granularity).
//
// Import resolution is delegated to a small embedded Python script
// (resolve_imports.py) run under whatever python3 is on PATH. It parses
// every tracked .py file's AST and resolves import targets - including
// relative imports (`from . import x`) via the stdlib's own
// importlib.util.resolve_name - against a registry of dotted module names
// built by walking the project tree ourselves. This never imports/executes
// the target project's own code (unlike a naive importlib.util.find_spec
// approach, which can trigger arbitrary package __init__.py side effects),
// so it works whether or not the project's dependencies are installed.
package pytestanalyzer

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hpscript/fastci/internal/graph"
	"github.com/hpscript/fastci/internal/runner"
)

//go:embed resolve_imports.py
var resolveImportsScript []byte

// Analyzer is the pytest implementation of analyzer.Analyzer.
type Analyzer struct{}

// New returns a pytest analyzer.
func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) Name() string { return "pytest" }

// Detect looks for pytest-specific configuration or a clear pytest
// dependency declaration: pytest.ini, a conftest.py, a "[tool:pytest]"
// section in setup.cfg, a "[tool.pytest.ini_options]" section in
// pyproject.toml, or the string "pytest" in a dependency manifest.
func (*Analyzer) Detect(dir string) (bool, error) {
	for _, name := range []string{"pytest.ini", "conftest.py", "tox.ini"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			if name == "tox.ini" {
				if containsToken(filepath.Join(dir, name), "pytest") {
					return true, nil
				}
				continue
			}
			return true, nil
		}
	}
	if containsToken(filepath.Join(dir, "setup.cfg"), "[tool:pytest]") {
		return true, nil
	}
	if containsToken(filepath.Join(dir, "pyproject.toml"), "[tool.pytest.ini_options]") {
		return true, nil
	}
	for _, name := range []string{"pyproject.toml", "requirements.txt", "requirements-dev.txt", "requirements-test.txt", "Pipfile"} {
		if containsToken(filepath.Join(dir, name), "pytest") {
			return true, nil
		}
	}
	return false, nil
}

func containsToken(path, token string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), token)
}

type resolverOutput struct {
	Files map[string]struct {
		Imports []string `json:"imports"`
		Dynamic bool     `json:"dynamic"`
	} `json:"files"`
}

// Build discovers every .py file under dir and resolves each one's imports
// by running resolve_imports.py under python3. See the package doc for why
// this is static (no project code is ever imported/executed).
func (*Analyzer) Build(dir string) (*graph.Graph, error) {
	py, err := pythonInterpreter()
	if err != nil {
		return nil, err
	}

	scriptPath, cleanup, err := writeTempScript()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	cmd := exec.Command(py, scriptPath, dir)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if stderr := strings.TrimSpace(string(ee.Stderr)); stderr != "" {
				return nil, fmt.Errorf("pytestanalyzer: resolving imports: %w\n%s", err, stderr)
			}
		}
		return nil, fmt.Errorf("pytestanalyzer: resolving imports: %w", err)
	}

	var parsed resolverOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("pytestanalyzer: parsing resolver output: %w", err)
	}

	g := graph.New()
	// Every node here is a single file (unlike Go/Cargo, where many files
	// legitimately share one package/crate node) - see the field doc.
	g.DisableDirFallback = true
	toAbs := func(relPath string) string {
		return filepath.Join(dir, filepath.FromSlash(relPath))
	}
	for relPath, entry := range parsed.Files {
		abs := toAbs(relPath)
		n := g.Node(abs)
		n.Files = []string{abs}
		if isTestFile(abs) {
			n.HasTestFiles = true
		}
		if entry.Dynamic {
			n.HasDynamicImport = true
		}
		for _, imp := range entry.Imports {
			impAbs := toAbs(imp)
			if impAbs == abs {
				continue
			}
			n.Imports[impAbs] = true
		}
	}

	g.IndexFiles()
	g.BuildImporters()
	return g, nil
}

func writeTempScript() (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "fastci-resolve-imports-*.py")
	if err != nil {
		return "", nil, fmt.Errorf("pytestanalyzer: %w", err)
	}
	if _, err := f.Write(resolveImportsScript); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("pytestanalyzer: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", nil, fmt.Errorf("pytestanalyzer: %w", err)
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func pythonInterpreter() (string, error) {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("pytestanalyzer: no python3/python interpreter found on PATH")
}

// testFileRE matches pytest's default python_files conventions:
// test_*.py or *_test.py. Custom python_files/python_files overrides in a
// pytest config aren't honored yet - see README.
var testFileRE = regexp.MustCompile(`^(test_.+|.+_test)\.py$`)

func isTestFile(absPath string) bool {
	return testFileRE.MatchString(filepath.Base(absPath))
}

var fullRunBasenames = map[string]bool{
	"pyproject.toml": true, "setup.py": true, "setup.cfg": true,
	"pytest.ini": true, "tox.ini": true, "conftest.py": true,
	"Pipfile": true, "Pipfile.lock": true, "poetry.lock": true, "uv.lock": true,
}

// FullRunFile reports whether a changed file should force a full test run:
// manifests, lockfiles, and conftest.py (whose fixtures can affect any test
// in their directory subtree, in ways the import graph doesn't capture) all
// qualify.
func (*Analyzer) FullRunFile(absPath string) bool {
	base := filepath.Base(absPath)
	if fullRunBasenames[base] {
		return true
	}
	if strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt") {
		return true
	}
	return false
}

// Ignorable reports whether a changed non-Python file is safe to ignore.
func (*Analyzer) Ignorable(absPath string) bool {
	return filepath.Ext(absPath) != ".py"
}

// AllTargets returns nil: pytest run with no path arguments already
// discovers and runs every test, so a full/--all run needs no explicit
// target list.
func (*Analyzer) AllTargets(dir string) ([]string, error) {
	return nil, nil
}

// RunTests runs pytest, preferring a local virtualenv's pytest if present
// and falling back to `pytest`/`python3 -m pytest` on PATH otherwise.
// Targets (test file paths) are passed as positional arguments, which
// pytest has always accepted directly - no CLI flag with version-dependent
// naming is involved.
func (*Analyzer) RunTests(ctx context.Context, dir string, targets []string, extraArgs []string) error {
	argv := pytestBinArgv(dir)
	argv = append(argv, targets...)
	argv = append(argv, extraArgs...)
	return runner.Run(ctx, runner.Options{Dir: dir, Argv: argv})
}

func pytestBinArgv(dir string) []string {
	for _, venvDir := range []string{".venv", "venv", "env"} {
		bin := filepath.Join(dir, venvDir, "bin", "pytest")
		if info, err := os.Stat(bin); err == nil && !info.IsDir() {
			return []string{bin}
		}
	}
	if path, err := exec.LookPath("pytest"); err == nil {
		return []string{path}
	}
	if py, err := pythonInterpreter(); err == nil {
		return []string{py, "-m", "pytest"}
	}
	return []string{"pytest"}
}
