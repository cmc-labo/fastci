package jestanalyzer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// This file implements a safety net for import()/require() calls whose
// target esbuild can't resolve statically - see the package doc and README
// for the general shape of the problem. Two cases matter:
//
//   - A fully opaque call, e.g. import(pick()): esbuild emits no diagnostic
//     and no edge at all; invisible to the metafile-based edge extraction in
//     Build().
//   - A template-literal call with a static directory prefix, e.g.
//     import(`./plugins/${name}`): esbuild converts this to an internal glob
//     import record *before* normal resolution, bypassing OnResolve plugins
//     entirely. If the directory doesn't exist, that's a fatal build error
//     (crashing the whole analyzer, not even a safe full-run fallback); if it
//     does exist, esbuild does bundle the matching files but records the edge
//     as a single "external" entry, which Build()'s existing
//     `if imp.External { continue }` silently drops.
//
// Both are handled the same way: scanDynamicImportSites finds these call
// sites by a lightweight source-text scan (esbuild's public Go API doesn't
// expose a parse tree), and dynamicImportNeutralizerPlugin rewrites each
// template-literal call's argument to an inert bare specifier via an OnLoad
// plugin *before* esbuild's parser ever sees the original text - a bare
// specifier resolves like a node_modules import under the Packages:
// PackagesExternal option already in use, i.e. external, zero errors, no
// glob classification. This makes the fatal-error case structurally
// impossible rather than caught-and-suppressed, and means the dropped-edge
// case never reaches esbuild's resolver at all: Build() instead resolves a
// safe superset of edges itself (every file under the matched directory) and
// marks fully opaque calls (and unmatched directory prefixes) with
// graph.Node.HasDynamicImport so impact analysis always includes them.

// templateCall is one import()/require() call site whose sole argument is a
// template literal containing a ${...} interpolation, e.g.
// `./plugins/${name}`. ArgStart/ArgEnd are the byte offsets (in the source
// this file was scanned from) of the whole argument, backticks included.
// StaticPrefix is the literal text between the opening backtick and the
// first "${".
type templateCall struct {
	ArgStart, ArgEnd int
	StaticPrefix     string
}

// dynamicImportScan is the result of scanning one source file for
// import()/require() call sites whose argument isn't a plain string/template
// literal esbuild can already resolve on its own.
type dynamicImportScan struct {
	// Opaque is true if the file contains at least one call whose argument
	// can't be reduced to a directory-scoped template pattern either - a
	// bare variable, a function call, string concatenation, etc.
	Opaque bool
	// TemplateCalls lists every call whose argument is exactly a template
	// literal with a static prefix before its first interpolation.
	TemplateCalls []templateCall
}

// scanDynamicImportSites scans src (a JS/TS/JSX/TSX source file) for
// import()/require() call sites and classifies each one. It's a lightweight
// hand-rolled scan, not a real parser, so it's deliberately biased toward
// false positives over false negatives: e.g. a plain method call named
// import or require (foo.import(x)) gets flagged even though it isn't a
// dynamic import, but a genuinely unresolvable call is never missed.
func scanDynamicImportSites(src []byte) dynamicImportScan {
	var scan dynamicImportScan
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			i += 2
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i = min(i+2, n)
		case c == '\'' || c == '"':
			i = scanStringLiteral(src, i)
		case c == '`':
			end, _, _ := scanTemplateLiteral(src, i)
			i = end
		case isIdentStart(c):
			start := i
			for i < n && isIdentPart(src[i]) {
				i++
			}
			word := string(src[start:i])
			if word != "import" && word != "require" {
				continue
			}
			j := i
			for j < n && isJSSpace(src[j]) {
				j++
			}
			if j >= n || src[j] != '(' {
				continue
			}
			i = classifyDynamicCall(src, j+1, &scan)
		default:
			i++
		}
	}
	return scan
}

// classifyDynamicCall inspects the argument of an import()/require() call
// starting at argStart (just past the open paren) and records it into scan
// if it isn't a plain literal esbuild already resolves on its own. Returns
// the index just past the parsed argument.
func classifyDynamicCall(src []byte, argStart int, scan *dynamicImportScan) int {
	n := len(src)
	i := argStart
	for i < n && isJSSpace(src[i]) {
		i++
	}
	if i >= n {
		scan.Opaque = true
		return i
	}

	switch src[i] {
	case '\'', '"':
		end := scanStringLiteral(src, i)
		if !isFollowedByCloseParen(src, end) {
			// Part of a larger expression, e.g. import("./x" + suffix) -
			// not a plain literal, esbuild won't resolve it either.
			scan.Opaque = true
		}
		return end
	case '`':
		end, hasInterp, prefix := scanTemplateLiteral(src, i)
		if !isFollowedByCloseParen(src, end) {
			scan.Opaque = true
			return end
		}
		if !hasInterp {
			return end // plain template, equivalent to a string literal.
		}
		scan.TemplateCalls = append(scan.TemplateCalls, templateCall{
			ArgStart:     i,
			ArgEnd:       end,
			StaticPrefix: prefix,
		})
		return end
	default:
		// A variable, function call, member expression, etc. - no way to
		// resolve this statically.
		scan.Opaque = true
		return i
	}
}

// scanStringLiteral returns the index just past the closing quote matching
// the opening quote at src[start] (' or "), honoring backslash escapes. If
// the string is unterminated, returns len(src).
func scanStringLiteral(src []byte, start int) int {
	quote := src[start]
	n := len(src)
	i := start + 1
	for i < n {
		c := src[i]
		if c == '\\' && i+1 < n {
			i += 2
			continue
		}
		if c == quote {
			return i + 1
		}
		i++
	}
	return n
}

// scanTemplateLiteral returns the index just past the closing backtick
// matching the opening backtick at src[start], whether any ${...}
// interpolation was found anywhere in it, and the literal text before the
// first interpolation (or before the closing backtick, if none). Nested
// interpolations - which can themselves contain further strings, template
// literals, comments, and braces - are skipped via skipInterpolation.
func scanTemplateLiteral(src []byte, start int) (end int, hasInterp bool, prefix string) {
	n := len(src)
	i := start + 1
	prefixEnd := -1
	for i < n {
		c := src[i]
		switch {
		case c == '\\' && i+1 < n:
			i += 2
		case c == '`':
			if prefixEnd < 0 {
				prefixEnd = i
			}
			return i + 1, hasInterp, string(src[start+1 : prefixEnd])
		case c == '$' && i+1 < n && src[i+1] == '{':
			if prefixEnd < 0 {
				prefixEnd = i
				hasInterp = true
			}
			i = skipInterpolation(src, i+2)
		default:
			i++
		}
	}
	if prefixEnd < 0 {
		prefixEnd = n
	}
	return n, hasInterp, string(src[start+1 : prefixEnd])
}

// skipInterpolation scans src[start:] as JS/TS code - which may contain
// strings, template literals, comments, and further nested braces - and
// returns the index just past the first unmatched '}', i.e. the close of a
// ${...} interpolation whose body starts at start.
func skipInterpolation(src []byte, start int) int {
	n := len(src)
	depth := 0
	i := start
	for i < n {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			i += 2
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i = min(i+2, n)
		case c == '\'' || c == '"':
			i = scanStringLiteral(src, i)
		case c == '`':
			end, _, _ := scanTemplateLiteral(src, i)
			i = end
		case c == '{':
			depth++
			i++
		case c == '}':
			if depth == 0 {
				return i + 1
			}
			depth--
			i++
		default:
			i++
		}
	}
	return n
}

func isFollowedByCloseParen(src []byte, i int) bool {
	n := len(src)
	for i < n && isJSSpace(src[i]) {
		i++
	}
	return i < n && src[i] == ')'
}

func isJSSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

// isIdentStart/isIdentPart deliberately treat any byte >= 0x80 (a UTF-8
// continuation or lead byte) as part of an identifier, so a non-ASCII
// identifier that happens to start with "import"/"require" (e.g.
// importÖnem) isn't misread as the bare keyword - see scanDynamicImportSites.
func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// rewrittenFile is the neutralized source dynamicImportNeutralizerPlugin
// substitutes for a file with one or more templateCall sites.
type rewrittenFile struct {
	contents string
	loader   api.Loader
}

var loaderByExt = map[string]api.Loader{
	".ts":  api.LoaderTS,
	".tsx": api.LoaderTSX,
	".jsx": api.LoaderJSX,
	".js":  api.LoaderJS,
	".mjs": api.LoaderJS,
	".cjs": api.LoaderJS,
}

func loaderForExt(ext string) api.Loader {
	if l, ok := loaderByExt[ext]; ok {
		return l
	}
	return api.LoaderJS
}

// dynamicImportNeutralizerPlugin serves the pre-rewritten contents in
// rewrites (keyed by absolute file path, matching OnLoadArgs.Path for the
// "file" namespace) in place of the real file contents, so esbuild's parser
// never sees the original template-literal dynamic import argument that
// would otherwise crash the build or produce a silently-dropped edge - see
// the package doc above. Every other file falls through to normal loading.
func dynamicImportNeutralizerPlugin(rewrites map[string]rewrittenFile) api.Plugin {
	return api.Plugin{
		Name: "fastci-dynamic-import-neutralizer",
		Setup: func(build api.PluginBuild) {
			build.OnLoad(api.OnLoadOptions{Filter: `\.(ts|tsx|jsx?|mjs|cjs)$`}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				rw, ok := rewrites[args.Path]
				if !ok {
					return api.OnLoadResult{}, nil
				}
				return api.OnLoadResult{Contents: &rw.contents, Loader: rw.loader}, nil
			})
		},
	}
}

// scanFilesForDynamicImports scans every file in files (absolute paths) for
// dynamic import()/require() call sites. It returns the set of files with at
// least one opaque call, the neutralized contents to feed esbuild for every
// file with at least one template call (keyed by absolute path, ready for
// dynamicImportNeutralizerPlugin), and the template calls found per file
// (for Build() to resolve into real edges once every file is a known node).
func scanFilesForDynamicImports(files []string) (opaqueFiles map[string]bool, rewrites map[string]rewrittenFile, templateCallsByFile map[string][]templateCall, err error) {
	opaqueFiles = map[string]bool{}
	rewrites = map[string]rewrittenFile{}
	templateCallsByFile = map[string][]templateCall{}

	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("jestanalyzer: reading %s: %w", f, err)
		}
		scan := scanDynamicImportSites(src)
		if scan.Opaque {
			opaqueFiles[f] = true
		}
		if len(scan.TemplateCalls) == 0 {
			continue
		}
		templateCallsByFile[f] = scan.TemplateCalls
		rewrites[f] = rewrittenFile{
			contents: neutralizeTemplateCalls(src, scan.TemplateCalls),
			loader:   loaderForExt(filepath.Ext(f)),
		}
	}
	return opaqueFiles, rewrites, templateCallsByFile, nil
}

// neutralizeTemplateCalls replaces every templateCall's argument (backticks
// included) with an inert bare specifier, so esbuild's parser never sees the
// original template literal - see the package doc above. Splices are applied
// in descending offset order so earlier offsets in the same file stay valid
// across multiple replacements.
func neutralizeTemplateCalls(src []byte, calls []templateCall) string {
	out := string(src)
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		out = out[:c.ArgStart] + `"__fastci_dynamic__"` + out[c.ArgEnd:]
	}
	return out
}

// templatePrefixDir resolves a templateCall's StaticPrefix to the directory
// it's rooted at, relative to the file containing the call - mirroring
// esbuild's own base-directory semantics for glob imports. A prefix ending
// in a path separator (e.g. "./plugins/") is already a directory path. A
// prefix ending mid filename (e.g. "./plugins/foo-${x}") is resolved to its
// parent directory. An empty prefix (the interpolation is the very first
// thing, e.g. `${x}`) resolves to the caller's own directory, since there's
// no partial filename fragment to strip.
func templatePrefixDir(callerFile, staticPrefix string) string {
	base := filepath.Dir(callerFile)
	if staticPrefix == "" {
		return base
	}
	prefix := filepath.FromSlash(staticPrefix)
	if strings.HasSuffix(prefix, string(filepath.Separator)) {
		return filepath.Join(base, prefix)
	}
	return filepath.Dir(filepath.Join(base, prefix))
}
