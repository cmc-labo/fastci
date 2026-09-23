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
func RefineRunFilter(repoRoot, base, dir string, changedFiles []string) (pattern string, ok bool) {
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

	testNames := reachableTestNames(fset, result.CallGraph, changed)
	if len(testNames) == 0 {
		return "", false
	}

	sort.Strings(testNames)
	return "^(" + strings.Join(testNames, "|") + ")$", true
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
// first, cycle-safe) and returns the deduplicated names of every Go test
// function - see isTestFunc - reached along the way, at any distance, in
// any package.
func reachableTestNames(fset *token.FileSet, cg *callgraph.Graph, roots []*ssa.Function) []string {
	visited := map[*callgraph.Node]bool{}
	var queue []*callgraph.Node
	for _, r := range roots {
		if n := cg.Nodes[r]; n != nil && !visited[n] {
			visited[n] = true
			queue = append(queue, n)
		}
	}

	names := map[string]bool{}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, edge := range n.In {
			caller := edge.Caller
			if visited[caller] {
				continue
			}
			visited[caller] = true
			queue = append(queue, caller)
			if fn := caller.Func; fn != nil && isTestFunc(fset, fn) {
				names[fn.Name()] = true
			}
		}
	}

	out := make([]string, 0, len(names))
	for n := range names {
		out = append(out, n)
	}
	return out
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
