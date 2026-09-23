// Package jsworkspace detects npm/yarn/pnpm workspace (monorepo) member
// packages and provides an esbuild plugin so jestanalyzer and
// vitestanalyzer can resolve a cross-package import like
// `import {x} from '@myorg/utils'` to that sibling package's real files,
// instead of it being unconditionally treated as an external
// node_modules import the way a genuine third-party dependency is.
//
// This is purely static: workspace membership comes from parsing the
// monorepo root's own package.json "workspaces" field or
// pnpm-workspace.yaml, and resolution is done by pointing esbuild's own
// resolver at the member package's directory - so, like the rest of
// fastci's JS/TS analysis, it never requires `npm install`/`pnpm
// install`/`yarn install` to have been run first.
package jsworkspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"go.yaml.in/yaml/v3"
)

// Members returns every workspace member package found in dir, mapping
// each package's declared name (from its own package.json "name" field,
// e.g. "@myorg/utils") to its absolute directory. dir must be the
// workspace root - the same place a root package.json "workspaces" field
// or pnpm-workspace.yaml would live; fastci does not search upward for
// one, matching the existing "run from the project root" convention for
// Jest/Vitest.
//
// This never fails: dir not being a workspace root at all, a malformed
// manifest, or a member directory whose package.json can't be read all
// just mean fewer (or zero) entries in the result, not an error - cross-
// package resolution is an optional enhancement on top of ordinary
// single-package analysis, which must keep working regardless.
func Members(dir string) map[string]string {
	patterns := workspacePatterns(dir)
	if len(patterns) == 0 {
		return nil
	}

	members := map[string]string{}
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") {
			continue // exclusion glob - see the package doc's scope note.
		}
		matches, err := filepath.Glob(filepath.Join(dir, filepath.FromSlash(pattern)))
		if err != nil {
			continue
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil || !info.IsDir() {
				continue
			}
			name, ok := packageName(m)
			if !ok {
				continue
			}
			members[name] = m
		}
	}
	return members
}

// workspacePatterns reads the workspace member glob patterns from
// whichever of npm/yarn's package.json "workspaces" field or pnpm's
// pnpm-workspace.yaml is present (checked in that order; a real project
// only ever has one or the other, matching its single package manager).
func workspacePatterns(dir string) []string {
	if patterns, ok := npmYarnWorkspacePatterns(dir); ok {
		return patterns
	}
	patterns, _ := pnpmWorkspacePatterns(dir)
	return patterns
}

type rootPackageJSON struct {
	Workspaces json.RawMessage `json:"workspaces"`
}

func npmYarnWorkspacePatterns(dir string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, false
	}
	var pkg rootPackageJSON
	if json.Unmarshal(data, &pkg) != nil || len(pkg.Workspaces) == 0 {
		return nil, false
	}

	var patterns []string
	if json.Unmarshal(pkg.Workspaces, &patterns) == nil {
		return patterns, true
	}
	var obj struct {
		Packages []string `json:"packages"`
	}
	if json.Unmarshal(pkg.Workspaces, &obj) == nil {
		return obj.Packages, true
	}
	return nil, false
}

func pnpmWorkspacePatterns(dir string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "pnpm-workspace.yaml"))
	if err != nil {
		return nil, false
	}
	var cfg struct {
		Packages []string `yaml:"packages"`
	}
	if yaml.Unmarshal(data, &cfg) != nil {
		return nil, false
	}
	return cfg.Packages, true
}

func packageName(memberDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(memberDir, "package.json"))
	if err != nil {
		return "", false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &pkg) != nil || pkg.Name == "" {
		return "", false
	}
	return pkg.Name, true
}

// pluginMarker tags a build.Resolve call Plugin made itself, so its own
// OnResolve hook (registered with a catch-all filter) doesn't try to
// re-match the already-substituted path against the member map again.
const pluginMarker = "fastci-workspace-packages-resolved"

// Plugin builds an esbuild plugin that resolves an import whose package
// name (the "@scope/name" or "name" prefix of the import path) matches a
// known workspace member to that package's real directory, using
// esbuild's own resolver from there - so its package.json
// "main"/"module"/"exports" field and normal extension/index resolution
// are all honored exactly as they would be for a real installed
// dependency. An import that doesn't match any member (a real third-party
// package, or a relative/absolute path) is left untouched, falling
// through to whatever default resolution the caller has configured (e.g.
// Packages: PackagesExternal).
func Plugin(members map[string]string) api.Plugin {
	return api.Plugin{
		Name: "fastci-workspace-packages",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if args.PluginData == pluginMarker {
					return api.OnResolveResult{}, nil
				}
				pkgName, subpath, ok := splitPackageImport(args.Path)
				if !ok {
					return api.OnResolveResult{}, nil
				}
				targetDir, ok := members[pkgName]
				if !ok {
					return api.OnResolveResult{}, nil
				}

				resolvePath := "."
				if subpath != "" {
					resolvePath = "./" + subpath
				}
				result := build.Resolve(resolvePath, api.ResolveOptions{
					ResolveDir: targetDir,
					Kind:       args.Kind,
					Namespace:  "file",
					PluginData: pluginMarker,
				})
				if len(result.Errors) > 0 {
					return api.OnResolveResult{}, nil
				}
				return api.OnResolveResult{Path: result.Path, External: result.External}, nil
			})
		},
	}
}

// splitPackageImport splits a bare import specifier into its package name
// and any subpath after it (e.g. "@myorg/utils/helpers" ->
// ("@myorg/utils", "helpers"); "left-pad" -> ("left-pad", "")). ok is
// false for a relative ("./x"), absolute ("/x"), or otherwise malformed
// (e.g. a lone "@scope" with no package name) path - only these count as
// "package paths" a workspace member's name could ever match.
func splitPackageImport(path string) (pkgName, subpath string, ok bool) {
	if path == "" || strings.HasPrefix(path, ".") || strings.HasPrefix(path, "/") {
		return "", "", false
	}
	if strings.HasPrefix(path, "@") {
		parts := strings.SplitN(path, "/", 3)
		if len(parts) < 2 {
			return "", "", false
		}
		pkgName = parts[0] + "/" + parts[1]
		if len(parts) == 3 {
			subpath = parts[2]
		}
		return pkgName, subpath, true
	}
	parts := strings.SplitN(path, "/", 2)
	pkgName = parts[0]
	if len(parts) == 2 {
		subpath = parts[1]
	}
	return pkgName, subpath, true
}
