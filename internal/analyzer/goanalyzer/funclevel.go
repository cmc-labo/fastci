package goanalyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/hpscript/fastci/internal/gitdiff"
)

// RefineRunFilter attempts function-level impact-analysis narrowing: given
// a diff that has already been through fastci's ordinary package-level
// impact analysis, it tries to compute a `go test -run` regex covering
// only the specific test functions that actually, transitively call a
// changed function - not just every test in an affected package.
//
// It builds a real call graph using RTA (Rapid Type Analysis,
// golang.org/x/tools/go/callgraph/rta), forward from every test function
// in the module as roots, then walks it backward from each changed
// function to find every (transitive) caller among those roots. RTA, not
// the simpler CHA algorithm, is essential: see the comment above the
// rta.Analyze call for why CHA is unusable here (it resolves any dynamic
// call site by signature alone against every function in the whole
// program, which for a plain function with a common signature reaches
// essentially everything).
//
// This is deliberately conservative, and always all-or-nothing for the
// whole diff: it returns ok=false - meaning "don't narrow, run the normal
// full set of tests for every target" - unless every single changed file
// is a non-test .go file whose diff hunks each fall entirely inside one
// existing function's body (no added/removed functions, no signature
// changes, no package-level declaration changes, no test files, nothing
// unparseable). RTA's own soundness gap is the same class as CHA's: it
// cannot see calls made via reflection, cgo, assembly, or //go:linkname -
// a genuine, if narrow, limitation for code using those mechanisms,
// accepted here in exchange for a large, real reduction in what has to
// run for the overwhelmingly common case of an ordinary function-body
// edit. It also never narrows down to *zero* tests for a target: if the
// call graph finds no reachable test at all for a changed function,
// that's treated the same as an unsafe diff - narrowing is skipped
// entirely, not resolved to "nothing to run".
func RefineRunFilter(repoRoot, base, dir string, changedFiles []string, targets []string) (pattern string, ok bool) {
	// golang.org/x/tools/go/ssa and go/callgraph/rta are real, heavy
	// static-analysis machinery exercised here against arbitrary
	// real-world Go code they weren't necessarily hardened against (unlike
	// go/packages/go list, which every other analyzer already leans on
	// safely) - a panic somewhere in there must never take down the
	// actual `fastci test` invocation over what's supposed to be a purely
	// optional narrowing. Recovering here and falling back to "don't
	// narrow" mirrors every other best-effort failure path in this
	// function; it's the one failure mode that can't be handled by an
	// ordinary error/ok return.
	defer func() {
		if recover() != nil {
			pattern, ok = "", false
		}
	}()
	return refineRunFilter(repoRoot, base, dir, changedFiles, targets)
}

func refineRunFilter(repoRoot, base, dir string, changedFiles []string, targets []string) (pattern string, ok bool) {
	goFiles, ok := onlyProductionGoFiles(changedFiles)
	if !ok {
		return "", false
	}

	patterns, err := Patterns(dir)
	if err != nil {
		return "", false
	}

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedModule |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax,
		Dir:   dir,
		Tests: true,
		Fset:  fset,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return "", false
	}
	var loadErrs bool
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if len(p.Errors) > 0 {
			loadErrs = true
		}
	})
	if loadErrs {
		return "", false
	}

	changedDecls, ok := changedFuncDecls(fset, pkgs, repoRoot, base, goFiles)
	if !ok {
		return "", false
	}

	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	changed := matchingFunctions(prog, changedDecls)
	if len(changed) == 0 {
		return "", false
	}

	testRoots := allTestFunctions(fset, prog)
	if len(testRoots) == 0 {
		return "", false
	}

	// RTA (Rapid Type Analysis), not CHA, is essential here: CHA resolves
	// every dynamic call site by *signature alone* against every function
	// in the entire built program ("CHA conservatively assumes that all
	// functions are address-taken" - see the cha package doc). For a
	// plain function with a common signature like func(int, int) int,
	// that's fatal - reverse-walking a CHA graph from a changed function
	// with that shape reaches essentially any dynamic call site anywhere
	// in the whole program (stdlib included) that happens to share it,
	// including ones with no real relationship to the changed function at
	// all, since CHA never actually confirms the function is taken as a
	// value anywhere. RTA instead discovers "address-taken" incrementally
	// by forward exploration from real roots (here, every test function in
	// the module) - a function only becomes a candidate callee of a
	// dynamic call site if it is *actually* observed as a value somewhere
	// in the reachable code, which an ordinary function only ever called
	// by name (as in the overwhelmingly common case) never is. That keeps
	// the graph sound without CHA's explosion of spurious edges.
	result := rta.Analyze(testRoots, true)
	if result == nil {
		return "", false
	}

	namesByPackage := reachableTestNames(fset, result.CallGraph, changed)
	if len(namesByPackage) == 0 {
		return "", false
	}

	// Every already-selected target must itself have at least one
	// reachable test, or this can't safely apply at all: the single -run
	// pattern this returns gets applied uniformly to every target in one
	// `go test` invocation (see the caller in cmd/fastci), so if even one
	// target's real path to the changed function crosses an interface or
	// closure boundary the walk above didn't trust (see
	// reachableTestNames), that target would come up with *zero* matching
	// tests - silently skipping a package impact analysis already
	// determined needs testing, exactly the "narrow to nothing" outcome
	// this package exists to never produce. Falling back for the whole
	// diff here, rather than only for that one target, keeps the contract
	// simple: either every target gets a trustworthy narrowed run, or none
	// of them do.
	var allNames []string
	for _, target := range targets {
		names := namesByPackage[target]
		if len(names) == 0 {
			return "", false
		}
		allNames = append(allNames, names...)
	}
	if len(allNames) == 0 {
		return "", false
	}

	sort.Strings(allNames)
	return "^(" + strings.Join(allNames, "|") + ")$", true
}

// allTestFunctions returns every Go test function (see isTestFunc) in the
// built program, to use as RTA's reachability roots.
func allTestFunctions(fset *token.FileSet, prog *ssa.Program) []*ssa.Function {
	var roots []*ssa.Function
	for fn := range ssautil.AllFunctions(prog) {
		if fn != nil && isTestFunc(fset, fn) {
			roots = append(roots, fn)
		}
	}
	return roots
}

// onlyProductionGoFiles reports whether every entry in changedFiles is a
// non-test .go file, returning that same list back for convenience. Any
// other kind of change (a _test.go file, a non-Go file not already
// screened out by the caller's own FullRunFile/Ignorable handling, ...)
// makes the whole diff ineligible for function-level narrowing.
func onlyProductionGoFiles(changedFiles []string) ([]string, bool) {
	if len(changedFiles) == 0 {
		return nil, false
	}
	for _, f := range changedFiles {
		if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
			return nil, false
		}
	}
	return changedFiles, true
}

// changedFuncDecls maps every changed file's diff hunks onto the single
// existing function whose body wholly contains each hunk, returning
// ok=false the moment any hunk can't be attributed that way.
func changedFuncDecls(fset *token.FileSet, pkgs []*packages.Package, repoRoot, base string, goFiles []string) ([]*ast.FuncDecl, bool) {
	fileFuncs := map[string][]*ast.FuncDecl{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, f := range p.Syntax {
			filename := fset.Position(f.Pos()).Filename
			for _, d := range f.Decls {
				if fd, isFunc := d.(*ast.FuncDecl); isFunc && fd.Body != nil {
					fileFuncs[filename] = append(fileFuncs[filename], fd)
				}
			}
		}
	})

	var changed []*ast.FuncDecl
	for _, file := range goFiles {
		hunks, hunksOK, err := gitdiff.ChangedHunks(repoRoot, base, file)
		if err != nil || !hunksOK {
			return nil, false
		}
		decls, known := fileFuncs[file]
		if !known {
			return nil, false // not part of the loaded package graph - be safe.
		}
		for _, hunk := range hunks {
			fd := containingFunc(fset, decls, hunk)
			if fd == nil {
				return nil, false
			}
			changed = append(changed, fd)
		}
	}
	return changed, true
}

// containingFunc returns the one FuncDecl among decls whose body (strictly
// after the opening brace's own line, through the closing brace's line,
// inclusive) fully contains hunk, or nil if no single function does -
// either because the hunk falls in a function's signature/doc-comment, at
// package level (imports, var/const/type declarations), or spans more
// than one function.
//
// The opening brace's own line is deliberately excluded from the safe
// zone, not just everything strictly above it: gofmt's standard style
// puts the closing `)` of the parameter list and the return type on that
// same line as the `{` (`func Add(a, b, c int) int {`), so a signature
// change - adding a parameter, say - shows up as a one-line hunk on
// exactly that line. Treating it as "inside the body" would let a
// signature change slip through undetected. A multi-line signature (where
// `{` sits alone on its own line) already excludes every signature line
// above it via the ordinary hunk.Start < bodyStart comparison; this only
// additionally closes the single-line-signature gap.
func containingFunc(fset *token.FileSet, decls []*ast.FuncDecl, hunk gitdiff.LineRange) *ast.FuncDecl {
	for _, fd := range decls {
		bodyStart := fset.Position(fd.Body.Lbrace).Line + 1
		bodyEnd := fset.Position(fd.Body.Rbrace).Line
		if bodyStart <= hunk.Start && hunk.End <= bodyEnd {
			return fd
		}
	}
	return nil
}

// matchingFunctions finds the *ssa.Function(s) corresponding to each
// changed FuncDecl, matched by comparing the declaration's name-identifier
// position against each SSA function's Pos() - both originate from the
// exact same *ast.File objects (packages.Load's Syntax, reused directly by
// ssautil.AllPackages), so this is an exact, not approximate, match. A
// generic function can yield more than one *ssa.Function (one per
// instantiation) sharing the same declaration position; all are included.
func matchingFunctions(prog *ssa.Program, changedDecls []*ast.FuncDecl) []*ssa.Function {
	wantPos := make(map[token.Pos]bool, len(changedDecls))
	for _, fd := range changedDecls {
		wantPos[fd.Name.Pos()] = true
	}

	var roots []*ssa.Function
	for fn := range ssautil.AllFunctions(prog) {
		if fn != nil && wantPos[fn.Pos()] {
			roots = append(roots, fn)
		}
	}
	return roots
}

// reachableTestNames walks the call graph backward from roots (breadth
// first, cycle-safe) and returns every Go test function - see isTestFunc -
// reached along the way, at any distance, grouped by the import path of
// the package it's declared in (so a caller can verify a specific target
// package actually has a match, not just that *some* package somewhere
// does).
//
// Two restrictions on the walk, both load-bearing for correctness, not
// just tuning:
//
//   - Only static edges are followed - see isStaticEdge. A dynamic edge
//     (through an interface method or a plain function/closure value) in
//     RTA's call graph is resolved per *call site*, not per calling
//     instance: if two different, unrelated callers each pass their own
//     closure through the very same higher-order function (an ordinary,
//     common pattern - a "run this closure and capture its output" test
//     helper, or testing.T.Run itself, which every subtest passes its own
//     closure through), RTA cannot tell the two apart, and the reverse
//     walk would cross from one caller into the other as if they called
//     each other directly. In a codebase using either pattern at all
//     (nearly all real Go test suites do, especially via t.Run), that
//     reduces to "every test is reachable from every other test" within a
//     handful of hops, defeating narrowing entirely. Following only
//     static edges gives up on precision through indirection (a change
//     reached only via an interface or closure boundary won't narrow -
//     the whole diff falls back to running everything, same as any other
//     "can't safely narrow" case) in exchange for the results that do
//     come back being trustworthy rather than an unbounded, often-total
//     over-approximation.
//   - Traversal stops at a node that is itself a test function - it does
//     not go on to explore that function's own incoming edges. A test
//     function is never called by another function in real, static code,
//     only by the testing package's own machinery, so there's nothing
//     useful above it to explore anyway; skipping it up front avoids ever
//     having to reason about whether that machinery's own dispatch (which
//     is dynamic, and would be excluded by the rule above regardless) is
//     safe to follow.
func reachableTestNames(fset *token.FileSet, cg *callgraph.Graph, roots []*ssa.Function) map[string][]string {
	visited := map[*callgraph.Node]bool{}
	var queue []*callgraph.Node
	for _, r := range roots {
		if n := cg.Nodes[r]; n != nil && !visited[n] {
			visited[n] = true
			queue = append(queue, n)
		}
	}

	byPackage := map[string]map[string]bool{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, edge := range n.In {
			if !isStaticEdge(edge) {
				continue // see the doc comment above.
			}
			caller := edge.Caller
			if visited[caller] {
				continue
			}
			visited[caller] = true
			if fn := caller.Func; fn != nil && isTestFunc(fset, fn) {
				pkgPath := fn.Package().Pkg.Path()
				if byPackage[pkgPath] == nil {
					byPackage[pkgPath] = map[string]bool{}
				}
				byPackage[pkgPath][fn.Name()] = true
				continue // stop here - a test function is never itself called by anything but the testing package's own machinery.
			}
			queue = append(queue, caller)
		}
	}

	out := make(map[string][]string, len(byPackage))
	for pkgPath, names := range byPackage {
		list := make([]string, 0, len(names))
		for n := range names {
			list = append(list, n)
		}
		out[pkgPath] = list
	}
	return out
}

// isStaticEdge reports whether edge's call site has a single, statically
// known callee - true for an ordinary call to a named top-level function
// or a method on a concrete (non-interface) type, false for a call
// through an interface method or a function/closure value, which RTA can
// only resolve to a *set* of possible callees rather than the one that
// was actually meant. See the reachableTestNames doc comment for why only
// the former is safe to treat as a real caller relationship here.
func isStaticEdge(edge *callgraph.Edge) bool {
	return edge.Site != nil && edge.Site.Common().StaticCallee() != nil
}

// isTestFunc reports whether fn is a function `go test` would actually
// run: declared in a _test.go file, named "Test" followed by an
// upper-case letter or nothing else (go/testing's own convention -
// "TestFoo" qualifies, "Testable" does not), taking exactly one parameter
// of type *testing.T and returning nothing.
func isTestFunc(fset *token.FileSet, fn *ssa.Function) bool {
	name := fn.Name()
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if len(name) > len("Test") {
		if r := name[len("Test")]; r >= 'a' && r <= 'z' {
			return false
		}
	}
	if !strings.HasSuffix(fset.Position(fn.Pos()).Filename, "_test.go") {
		return false
	}

	sig := fn.Signature
	if sig.Params().Len() != 1 || sig.Results().Len() != 0 {
		return false
	}
	ptr, isPtr := sig.Params().At(0).Type().(*types.Pointer)
	if !isPtr {
		return false
	}
	named, isNamed := ptr.Elem().(*types.Named)
	if !isNamed || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	return named.Obj().Pkg().Path() == "testing" && named.Obj().Name() == "T"
}
