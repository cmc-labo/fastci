package goanalyzer_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/goanalyzer"
)

func initGoRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "-q", "-b", "main")
	runGitCmd(t, dir, "config", "user.email", "t@example.com")
	runGitCmd(t, dir, "config", "user.name", "Test")
	return dir
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFuncLevelFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitFuncLevel(t *testing.T, dir, msg string) {
	t.Helper()
	runGitCmd(t, dir, "add", "-A")
	runGitCmd(t, dir, "commit", "-q", "-m", msg)
}

const calcGoV1 = `package calc

func Add(a, b int) int {
	return a + b
}

func Multiply(a, b int) int {
	return a * b
}

func ComputeViaAdd(a, b int) int {
	return Add(a, b) + 1
}
`

const calcTestGo = `package calc

import "testing"

func TestComputeViaAdd(t *testing.T) {
	if ComputeViaAdd(2, 3) != 6 {
		t.Fatal("bad")
	}
}

func TestMultiply(t *testing.T) {
	if Multiply(2, 3) != 6 {
		t.Fatal("bad")
	}
}
`

func setupCalcModule(t *testing.T) string {
	t.Helper()
	dir := initGoRepo(t)
	writeFuncLevelFile(t, dir, "go.mod", "module calc\n\ngo 1.21\n")
	writeFuncLevelFile(t, dir, "calc.go", calcGoV1)
	writeFuncLevelFile(t, dir, "calc_test.go", calcTestGo)
	commitFuncLevel(t, dir, "init")
	return dir
}

// TestRefineRunFilterNarrowsToTransitiveCaller is the core positive case:
// Add's body changes; Add is called (transitively, through ComputeViaAdd)
// only by TestComputeViaAdd, never by TestMultiply. A real call graph is
// the only way to know that - the two are otherwise unrelated functions in
// the same file/package.
func TestRefineRunFilterNarrowsToTransitiveCaller(t *testing.T) {
	dir := setupCalcModule(t)
	writeFuncLevelFile(t, dir, "calc.go", `package calc

func Add(a, b int) int {
	return a + b + 0 // changed
}

func Multiply(a, b int) int {
	return a * b
}

func ComputeViaAdd(a, b int) int {
	return Add(a, b) + 1
}
`)

	pattern, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "calc.go")}, []string{"calc"})
	if !ok {
		t.Fatal("RefineRunFilter: ok = false, want true")
	}
	if pattern != "^(TestComputeViaAdd)$" {
		t.Errorf("pattern = %q, want %q", pattern, "^(TestComputeViaAdd)$")
	}
}

// TestRefineRunFilterNarrowsMultiplyOnly is the mirror case, changing the
// other, independent function.
func TestRefineRunFilterNarrowsMultiplyOnly(t *testing.T) {
	dir := setupCalcModule(t)
	writeFuncLevelFile(t, dir, "calc.go", `package calc

func Add(a, b int) int {
	return a + b
}

func Multiply(a, b int) int {
	return a * b * 1 // changed
}

func ComputeViaAdd(a, b int) int {
	return Add(a, b) + 1
}
`)

	pattern, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "calc.go")}, []string{"calc"})
	if !ok {
		t.Fatal("RefineRunFilter: ok = false, want true")
	}
	if pattern != "^(TestMultiply)$" {
		t.Errorf("pattern = %q, want %q", pattern, "^(TestMultiply)$")
	}
}

// TestRefineRunFilterFallsBackOnSignatureChange changes Add's signature
// (not just its body) - this can ripple beyond what the call graph alone
// captures (every caller needs updating too), so it must be treated as
// unsafe to narrow, the same as any other non-body-only change.
func TestRefineRunFilterFallsBackOnSignatureChange(t *testing.T) {
	dir := setupCalcModule(t)
	writeFuncLevelFile(t, dir, "calc.go", `package calc

func Add(a, b, c int) int {
	return a + b + c
}

func Multiply(a, b int) int {
	return a * b
}

func ComputeViaAdd(a, b int) int {
	return Add(a, b, 0) + 1
}
`)

	_, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "calc.go")}, []string{"calc"})
	if ok {
		t.Error("RefineRunFilter: ok = true for a signature change, want false (unsafe to narrow)")
	}
}

// TestRefineRunFilterFallsBackOnNewFunction adds a whole new function -
// the hunk doesn't fall inside any existing function's body at all.
func TestRefineRunFilterFallsBackOnNewFunction(t *testing.T) {
	dir := setupCalcModule(t)
	writeFuncLevelFile(t, dir, "calc.go", calcGoV1+`
func Subtract(a, b int) int {
	return a - b
}
`)

	_, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "calc.go")}, []string{"calc"})
	if ok {
		t.Error("RefineRunFilter: ok = true for a newly added function, want false (unsafe to narrow)")
	}
}

// TestRefineRunFilterFallsBackOnTestFileChange excludes _test.go changes
// entirely from narrowing eligibility - see the package doc.
func TestRefineRunFilterFallsBackOnTestFileChange(t *testing.T) {
	dir := setupCalcModule(t)
	writeFuncLevelFile(t, dir, "calc_test.go", calcTestGo+`
func TestNew(t *testing.T) {}
`)

	_, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "calc_test.go")}, []string{"calc"})
	if ok {
		t.Error("RefineRunFilter: ok = true for a _test.go change, want false")
	}
}

// TestRefineRunFilterFallsBackWhenNoTestReachesTheChange covers a function
// with no test coverage at all: narrowing to "run nothing" would be the
// riskiest possible outcome, so this must fall back to "don't narrow"
// instead.
func TestRefineRunFilterFallsBackWhenNoTestReachesTheChange(t *testing.T) {
	dir := setupCalcModule(t)
	writeFuncLevelFile(t, dir, "calc.go", calcGoV1+`
func Untested(a, b int) int {
	return a - b - 0 // changed, but nothing calls or tests this
}
`)
	commitFuncLevel(t, dir, "add untested func")
	writeFuncLevelFile(t, dir, "calc.go", calcGoV1+`
func Untested(a, b int) int {
	return a - b - 1 // now changed
}
`)

	_, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "calc.go")}, []string{"calc"})
	if ok {
		t.Error("RefineRunFilter: ok = true for a function no test reaches, want false")
	}
}

const opGoV1 = `package calc

type Op interface {
	Do(a, b int) int
}

type AddOp struct{}

func (AddOp) Do(a, b int) int {
	return a + b
}

type SubOp struct{}

func (SubOp) Do(a, b int) int {
	return a - b
}

func RunOp(op Op, a, b int) int {
	return op.Do(a, b)
}
`

const opTestGo = `package calc

import "testing"

func TestRunOpWithAdd(t *testing.T) {
	if RunOp(AddOp{}, 2, 3) != 5 {
		t.Fatal("bad")
	}
}

func TestRunOpWithSub(t *testing.T) {
	if RunOp(SubOp{}, 5, 3) != 2 {
		t.Fatal("bad")
	}
}
`

// TestRefineRunFilterFollowsInterfaceDispatch exercises interface
// dispatch, which reachableTestNames deliberately never traverses (see its
// doc comment): RTA resolves a dynamic call site's possible callees per
// *call site*, not per calling instance, so two unrelated callers sharing
// one indirection point - here, RunOp's single `op.Do(a, b)` call site,
// reached from both TestRunOpWithAdd (passing AddOp) and TestRunOpWithSub
// (passing SubOp) - can't be told apart, and in a real codebase that
// quickly cascades into "every test reaches every other test" (this is
// exactly what real dogfooding against fastci's own test suite surfaced,
// via testing.T.Run and ordinary closure-taking helpers - the same
// mechanism, just far more of it). So this diff is expected to fall back
// entirely rather than resolve to "both tests, over-approximated": a
// change reachable only through an interface (or closure) boundary isn't
// narrowed at all, the same as any other diff RefineRunFilter can't
// safely narrow.
func TestRefineRunFilterFollowsInterfaceDispatch(t *testing.T) {
	dir := initGoRepo(t)
	writeFuncLevelFile(t, dir, "go.mod", "module calc\n\ngo 1.21\n")
	writeFuncLevelFile(t, dir, "op.go", opGoV1)
	writeFuncLevelFile(t, dir, "op_test.go", opTestGo)
	commitFuncLevel(t, dir, "init")

	writeFuncLevelFile(t, dir, "op.go", `package calc

type Op interface {
	Do(a, b int) int
}

type AddOp struct{}

func (AddOp) Do(a, b int) int {
	return a + b + 0 // changed
}

type SubOp struct{}

func (SubOp) Do(a, b int) int {
	return a - b
}

func RunOp(op Op, a, b int) int {
	return op.Do(a, b)
}
`)

	_, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "op.go")}, []string{"calc"})
	if ok {
		t.Error("RefineRunFilter: ok = true, want false - see the test's doc comment for why interface dispatch must fall back")
	}
}

// TestRefineRunFilterCrossesPackageBoundaries verifies the call graph is
// built for the whole module, not just the package containing the changed
// file - here the change is in package "leaf", but only a test in package
// "consumer" (which imports leaf) calls it.
func TestRefineRunFilterCrossesPackageBoundaries(t *testing.T) {
	dir := initGoRepo(t)
	writeFuncLevelFile(t, dir, "go.mod", "module calc\n\ngo 1.21\n")
	writeFuncLevelFile(t, dir, "leaf/leaf.go", `package leaf

func Double(x int) int {
	return x * 2
}
`)
	writeFuncLevelFile(t, dir, "consumer/consumer.go", `package consumer

import "calc/leaf"

func UseDouble(x int) int {
	return leaf.Double(x)
}
`)
	writeFuncLevelFile(t, dir, "consumer/consumer_test.go", `package consumer

import "testing"

func TestUseDouble(t *testing.T) {
	if UseDouble(3) != 6 {
		t.Fatal("bad")
	}
}
`)
	commitFuncLevel(t, dir, "init")

	writeFuncLevelFile(t, dir, "leaf/leaf.go", `package leaf

func Double(x int) int {
	return x * 2 + 0 // changed
}
`)

	pattern, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "leaf", "leaf.go")}, []string{"calc/consumer"})
	if !ok {
		t.Fatal("RefineRunFilter: ok = false, want true")
	}
	if pattern != "^(TestUseDouble)$" {
		t.Errorf("pattern = %q, want %q", pattern, "^(TestUseDouble)$")
	}
}

// TestRefineRunFilterFallsBackWhenAnyTargetHasNoReachableTest reproduces a
// real bug found during manual dogfooding: an already-selected target
// package whose *only* real path to the changed function crosses an
// indirect (interface, or here, plain function-value) call - which
// reachableTestNames correctly refuses to trust, see its doc comment -
// must never be silently left with zero matching tests once a single
// combined -run pattern gets applied to every target together. Here,
// package "wrapper" imports "calc" and genuinely, correctly gets selected
// by ordinary package-level impact analysis, but only calls calc.Add by
// passing it as a first-class function value through another function
// that invokes it indirectly - a path function-level narrowing can't
// verify. Even though "calc" itself has a perfectly good static-only
// reachable test (TestComputeViaAdd), applying that pattern alone to
// "wrapper" too would silently run zero of its tests. The whole diff must
// fall back instead.
func TestRefineRunFilterFallsBackWhenAnyTargetHasNoReachableTest(t *testing.T) {
	dir := setupCalcModule(t)
	writeFuncLevelFile(t, dir, "wrapper/wrapper.go", `package wrapper

import "calc"

type opFunc func(int, int) int

func RunViaFuncValue(op opFunc, a, b int) int {
	return op(a, b)
}

func UseAdd(a, b int) int {
	return RunViaFuncValue(calc.Add, a, b)
}
`)
	writeFuncLevelFile(t, dir, "wrapper/wrapper_test.go", `package wrapper

import "testing"

func TestUseAdd(t *testing.T) {
	if UseAdd(2, 3) != 5 {
		t.Fatal("bad")
	}
}
`)
	commitFuncLevel(t, dir, "add wrapper package")

	writeFuncLevelFile(t, dir, "calc.go", `package calc

func Add(a, b int) int {
	return a + b + 0 // changed
}

func Multiply(a, b int) int {
	return a * b
}

func ComputeViaAdd(a, b int) int {
	return Add(a, b) + 1
}
`)

	_, ok := goanalyzer.RefineRunFilter(dir, "", dir, []string{filepath.Join(dir, "calc.go")}, []string{"calc", "wrapper"})
	if ok {
		t.Error("RefineRunFilter: ok = true, want false - \"wrapper\" has no statically-reachable test even though \"calc\" does")
	}
}
