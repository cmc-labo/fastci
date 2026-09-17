package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LifecycleScripts flags installed npm/pnpm/yarn dependencies whose own
// package.json defines an install-time lifecycle script (preinstall,
// install, or postinstall) - the mechanism behind real supply-chain
// attacks like event-stream (2018) and ua-parser-js (2021), where
// malicious code ran automatically the moment the package was installed,
// before any application code ever executed.
//
// A lifecycle script isn't inherently malicious - plenty of legitimate
// packages (esbuild, puppeteer, husky, core-js...) use one to fetch a
// prebuilt binary or set up git hooks - so this doesn't try to judge
// intent, only surface which dependencies can run arbitrary code at
// install time so a human can look.
//
// Unlike the other Checkers, this one doesn't delegate to an existing
// ecosystem tool: no widely-adopted, free, offline tool does exactly this
// specific check, so fastci does the (small, mechanical) package.json
// scanning itself, rather than reimplementing vulnerability *detection*,
// which is what the other Checkers avoid doing.
type LifecycleScripts struct{}

func (LifecycleScripts) Name() string { return "npm/pnpm/yarn lifecycle scripts" }

func (LifecycleScripts) Detect(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, "package.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (LifecycleScripts) BinaryAvailable(dir string) (bool, string) {
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); os.IsNotExist(err) {
		return false, `no node_modules directory found - run "npm install" (or pnpm/yarn) first so fastci can scan what would actually be installed`
	}
	return true, ""
}

var lifecycleScriptNames = []string{"preinstall", "install", "postinstall"}

type npmPackageManifest struct {
	Name    string            `json:"name"`
	Version string            `json:"version"`
	Scripts map[string]string `json:"scripts"`
}

// Run walks node_modules for every package.json and records which packages
// define a preinstall/install/postinstall script. It reads what's already
// on disk rather than running an install itself: fastci shouldn't trigger
// arbitrary code execution as a side effect of a security scan.
func (LifecycleScripts) Run(ctx context.Context, dir string) (Result, error) {
	root := filepath.Join(dir, "node_modules")

	type hit struct {
		name, version string
		scripts       []string
	}
	var hits []hit

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() || d.Name() != "package.json" {
			return nil
		}
		// Skip a dependency's own nested node_modules/.bin shims and any
		// package.json belonging to a *nested* node_modules tree's own
		// transitive dependency of a transitive dependency - those are
		// covered on their own turn as WalkDir descends into them, so
		// nothing is special-cased here; every package.json found gets
		// checked once, wherever it lives in the tree.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		var m npmPackageManifest
		if json.Unmarshal(data, &m) != nil {
			return nil
		}
		var found []string
		for _, name := range lifecycleScriptNames {
			if s := strings.TrimSpace(m.Scripts[name]); s != "" {
				found = append(found, name)
			}
		}
		if len(found) > 0 {
			pkgName := m.Name
			if pkgName == "" {
				// Fall back to the directory name (relative to
				// node_modules) so a malformed/unnamed manifest still gets
				// reported instead of silently blending into the rest.
				rel, _ := filepath.Rel(root, filepath.Dir(path))
				pkgName = rel
			}
			hits = append(hits, hit{name: pkgName, version: m.Version, scripts: found})
		}
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("scanning %s: %w", root, err)
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].name < hits[j].name })

	res := Result{CheckerName: "LifecycleScripts"}
	if len(hits) == 0 {
		res.Output = "no installed dependency defines a preinstall/install/postinstall script"
		return res, nil
	}

	res.FoundIssues = true
	verb := "define"
	noun := "dependencies"
	if len(hits) == 1 {
		verb = "defines"
		noun = "dependency"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d installed %s %s an install-time lifecycle script - not necessarily malicious, but each one runs code automatically during install, so review any you don't recognize:\n", len(hits), noun, verb)
	for _, h := range hits {
		fmt.Fprintf(&b, "  %s@%s: %s\n", h.name, h.version, strings.Join(h.scripts, ", "))
	}
	res.Output = strings.TrimRight(b.String(), "\n")
	return res, nil
}
