package jsworkspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hpscript/fastci/internal/analyzer/jsworkspace"
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

func TestMembersNpmYarnArrayForm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"root","workspaces":["packages/*"]}`)
	writeFile(t, filepath.Join(dir, "packages", "a", "package.json"), `{"name":"@org/a"}`)
	writeFile(t, filepath.Join(dir, "packages", "b", "package.json"), `{"name":"@org/b"}`)

	members := jsworkspace.Members(dir)
	if len(members) != 2 {
		t.Fatalf("Members = %v, want 2 entries", members)
	}
	if members["@org/a"] != filepath.Join(dir, "packages", "a") {
		t.Errorf("@org/a -> %q, want %q", members["@org/a"], filepath.Join(dir, "packages", "a"))
	}
	if members["@org/b"] != filepath.Join(dir, "packages", "b") {
		t.Errorf("@org/b -> %q, want %q", members["@org/b"], filepath.Join(dir, "packages", "b"))
	}
}

func TestMembersNpmYarnObjectForm(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"root","workspaces":{"packages":["packages/*"],"nohoist":["**/react-native"]}}`)
	writeFile(t, filepath.Join(dir, "packages", "a", "package.json"), `{"name":"@org/a"}`)

	members := jsworkspace.Members(dir)
	if members["@org/a"] != filepath.Join(dir, "packages", "a") {
		t.Errorf("Members = %v, want @org/a -> %s", members, filepath.Join(dir, "packages", "a"))
	}
}

func TestMembersPnpmWorkspaceYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pnpm-workspace.yaml"), "packages:\n  - packages/*\n  - apps/*\n")
	writeFile(t, filepath.Join(dir, "packages", "a", "package.json"), `{"name":"@org/a"}`)
	writeFile(t, filepath.Join(dir, "apps", "web", "package.json"), `{"name":"@org/web"}`)

	members := jsworkspace.Members(dir)
	if len(members) != 2 {
		t.Fatalf("Members = %v, want 2 entries", members)
	}
	if members["@org/a"] != filepath.Join(dir, "packages", "a") {
		t.Errorf("@org/a -> %q, want %q", members["@org/a"], filepath.Join(dir, "packages", "a"))
	}
	if members["@org/web"] != filepath.Join(dir, "apps", "web") {
		t.Errorf("@org/web -> %q, want %q", members["@org/web"], filepath.Join(dir, "apps", "web"))
	}
}

func TestMembersNpmYarnTakesPriorityOverPnpm(t *testing.T) {
	// A real project only ever has one package manager's manifest, but the
	// lookup order should still be deterministic if somehow both exist.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"root","workspaces":["from-npm/*"]}`)
	writeFile(t, filepath.Join(dir, "pnpm-workspace.yaml"), "packages:\n  - from-pnpm/*\n")
	writeFile(t, filepath.Join(dir, "from-npm", "a", "package.json"), `{"name":"@org/a"}`)
	writeFile(t, filepath.Join(dir, "from-pnpm", "b", "package.json"), `{"name":"@org/b"}`)

	members := jsworkspace.Members(dir)
	if _, ok := members["@org/a"]; !ok {
		t.Error(`Members should include "@org/a" from package.json's "workspaces"`)
	}
	if _, ok := members["@org/b"]; ok {
		t.Error(`Members should not include "@org/b" from pnpm-workspace.yaml when package.json "workspaces" is present`)
	}
}

func TestMembersNoWorkspaceConfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"root"}`)

	members := jsworkspace.Members(dir)
	if len(members) != 0 {
		t.Errorf("Members = %v, want none - no workspace config at all", members)
	}
}

func TestMembersSkipsMemberDirWithoutPackageJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"root","workspaces":["packages/*"]}`)
	// A directory matching the glob but with no package.json at all (e.g.
	// a stray non-package directory under packages/) shouldn't be added
	// or cause an error.
	if err := os.MkdirAll(filepath.Join(dir, "packages", "not-a-package"), 0o755); err != nil {
		t.Fatal(err)
	}

	members := jsworkspace.Members(dir)
	if len(members) != 0 {
		t.Errorf("Members = %v, want none", members)
	}
}

func TestMembersMalformedManifestDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{not valid json`)

	members := jsworkspace.Members(dir)
	if len(members) != 0 {
		t.Errorf("Members = %v, want none for a malformed package.json", members)
	}
}
