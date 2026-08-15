package jestanalyzer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// mapperEntry is one moduleNameMapper rule: a compiled regex and the
// replacement template(s) to try (in order) when it matches. A value in
// Jest config can be a single string or an array of fallback candidates.
type mapperEntry struct {
	pattern *regexp.Regexp
	targets []string
}

// mapperMarker tags a build.Resolve call this plugin made itself, so its
// own OnResolve hook (registered with a catch-all filter) doesn't try to
// re-match the already-substituted path against the same rules again.
const mapperMarker = "fastci-module-name-mapper-resolved"

// loadModuleNameMapper reads a Jest moduleNameMapper configuration, if any,
// from jest.config.json or the package.json "jest" field (in that order -
// matching Jest's own precedence for JSON-shaped config). Patterns that
// don't compile as Go regexps (some PCRE-only syntax Jest's JS regex
// engine supports isn't representable in Go's RE2) are skipped rather than
// failing the whole build, since falling back to no mapping for that one
// rule is strictly safer than refusing to analyze the project at all.
func loadModuleNameMapper(dir string) ([]mapperEntry, error) {
	raw, err := moduleNameMapperRaw(dir)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}

	pairs, err := decodeOrderedStringMap(raw)
	if err != nil {
		return nil, fmt.Errorf("jestanalyzer: parsing moduleNameMapper: %w", err)
	}

	entries := make([]mapperEntry, 0, len(pairs))
	for _, p := range pairs {
		re, err := regexp.Compile(p.Key)
		if err != nil {
			continue
		}
		entries = append(entries, mapperEntry{pattern: re, targets: p.Values})
	}
	return entries, nil
}

type jestConfigFile struct {
	ModuleNameMapper json.RawMessage `json:"moduleNameMapper"`
}

func moduleNameMapperRaw(dir string) (json.RawMessage, error) {
	if data, err := os.ReadFile(filepath.Join(dir, "jest.config.json")); err == nil {
		var cfg jestConfigFile
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("jestanalyzer: parsing jest.config.json: %w", err)
		}
		return cfg.ModuleNameMapper, nil
	}

	pkg, err := readPackageJSON(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(pkg.Jest) == 0 {
		return nil, nil
	}
	var cfg jestConfigFile
	if err := json.Unmarshal(pkg.Jest, &cfg); err != nil {
		return nil, fmt.Errorf(`jestanalyzer: parsing package.json "jest" field: %w`, err)
	}
	return cfg.ModuleNameMapper, nil
}

type orderedEntry struct {
	Key    string
	Values []string
}

// decodeOrderedStringMap decodes a JSON object into key/value pairs in
// their original declaration order - encoding/json's map[string]T decoding
// doesn't preserve this, but Jest documents moduleNameMapper as matched in
// declaration order with the first match winning, so order is significant.
func decodeOrderedStringMap(raw json.RawMessage) ([]orderedEntry, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}

	var out []orderedEntry
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected a string key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		values, err := decodeStringOrSlice(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, orderedEntry{Key: key, Values: values})
	}
	return out, nil
}

func decodeStringOrSlice(raw json.RawMessage) ([]string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}, nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

var captureGroupRefRE = regexp.MustCompile(`\$(\d+)`)

// expandTarget fills in a moduleNameMapper replacement template: <rootDir>
// becomes dir, and $1/$2/... become the corresponding regex capture group
// from match (FindStringSubmatch's result for the matched specifier).
func expandTarget(template, dir string, match []string) string {
	out := strings.ReplaceAll(template, "<rootDir>", dir)
	return captureGroupRefRE.ReplaceAllStringFunc(out, func(s string) string {
		idx, err := strconv.Atoi(s[1:])
		if err != nil || idx >= len(match) {
			return ""
		}
		return match[idx]
	})
}

// moduleNameMapperPlugin builds an esbuild plugin that re-resolves any
// import specifier matching a moduleNameMapper rule to its mapped target,
// using esbuild's own resolver (so the target still goes through normal
// extension/index resolution) before falling back to esbuild's default
// resolution for anything that doesn't match, or whose mapped target(s)
// don't actually resolve to a file.
func moduleNameMapperPlugin(dir string, entries []mapperEntry) api.Plugin {
	return api.Plugin{
		Name: "fastci-module-name-mapper",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if args.PluginData == mapperMarker {
					return api.OnResolveResult{}, nil
				}
				for _, e := range entries {
					m := e.pattern.FindStringSubmatch(args.Path)
					if m == nil {
						continue
					}
					for _, tmpl := range e.targets {
						target := expandTarget(tmpl, dir, m)
						result := build.Resolve(target, api.ResolveOptions{
							ResolveDir: dir,
							Kind:       args.Kind,
							Namespace:  "file",
							PluginData: mapperMarker,
						})
						if len(result.Errors) == 0 {
							return api.OnResolveResult{Path: result.Path, External: result.External}, nil
						}
					}
				}
				return api.OnResolveResult{}, nil
			})
		},
	}
}
