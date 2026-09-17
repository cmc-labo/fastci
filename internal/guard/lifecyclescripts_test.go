package guard_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/guard"
)

func TestLifecycleScriptsDetect(t *testing.T) {
	dir := t.TempDir()
	ok, err := guard.LifecycleScripts{}.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("Detect = true on an empty directory, want false")
	}

	writeFile(t, filepath.Join(dir, "package.json"), "{}")
	ok, err = guard.LifecycleScripts{}.Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("Detect = false after creating package.json, want true")
	}
}

func TestLifecycleScriptsBinaryAvailableRequiresNodeModules(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), "{}")

	ok, hint := guard.LifecycleScripts{}.BinaryAvailable(dir)
	if ok {
		t.Error("BinaryAvailable = true without node_modules, want false")
	}
	if !strings.Contains(hint, "node_modules") {
		t.Errorf("hint = %q, want it to mention node_modules", hint)
	}

	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	ok, _ = guard.LifecycleScripts{}.BinaryAvailable(dir)
	if !ok {
		t.Error("BinaryAvailable = false with node_modules present, want true")
	}
}

func TestLifecycleScriptsRunFindsPostinstall(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), "{}")

	// A clean dependency with no lifecycle scripts at all.
	writeFile(t, filepath.Join(dir, "node_modules", "left-pad", "package.json"),
		`{"name":"left-pad","version":"1.3.0","scripts":{"test":"echo ok"}}`)

	// A dependency with a postinstall script - this is what a real
	// event-stream/ua-parser-js-style attack would look like: an install
	// step that runs automatically.
	writeFile(t, filepath.Join(dir, "node_modules", "sketchy-pkg", "package.json"),
		`{"name":"sketchy-pkg","version":"2.1.0","scripts":{"postinstall":"node ./inject.js"}}`)

	// A scoped package, nested one directory deeper (@org/name), also with
	// a preinstall script, to check the walk isn't scoped-package-blind.
	writeFile(t, filepath.Join(dir, "node_modules", "@org", "native-thing", "package.json"),
		`{"name":"@org/native-thing","version":"0.1.0","scripts":{"preinstall":"node-gyp rebuild"}}`)

	res, err := guard.LifecycleScripts{}.Run(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.FoundIssues {
		t.Fatalf("FoundIssues = false, want true; output:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "sketchy-pkg@2.1.0") || !strings.Contains(res.Output, "postinstall") {
		t.Errorf("output missing sketchy-pkg's postinstall script:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "@org/native-thing@0.1.0") || !strings.Contains(res.Output, "preinstall") {
		t.Errorf("output missing @org/native-thing's preinstall script:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "left-pad") {
		t.Errorf("output mentions left-pad, which has no lifecycle script:\n%s", res.Output)
	}
}

func TestLifecycleScriptsRunCleanWhenNoneDefined(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), "{}")
	writeFile(t, filepath.Join(dir, "node_modules", "left-pad", "package.json"),
		`{"name":"left-pad","version":"1.3.0"}`)

	res, err := guard.LifecycleScripts{}.Run(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.FoundIssues {
		t.Errorf("FoundIssues = true, want false; output:\n%s", res.Output)
	}
}
