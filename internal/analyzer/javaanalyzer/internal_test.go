package javaanalyzer

import (
	"path/filepath"
	"testing"
)

// TestFqcnFromPath covers the standard Maven/Gradle source-root
// convention this derives a fully-qualified class name from, and its
// fallback when a file isn't under either recognized root - see the
// function's own doc comment for why FQCN (not just the simple class
// name) matters for both precision and Gradle's own --tests matching.
func TestFqcnFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "main source root",
			path: filepath.Join("repo", "module-a", "src", "main", "java", "com", "example", "a", "Calc.java"),
			want: "com.example.a.Calc",
		},
		{
			name: "test source root",
			path: filepath.Join("repo", "module-b", "src", "test", "java", "com", "example", "b", "ConsumerTest.java"),
			want: "com.example.b.ConsumerTest",
		},
		{
			name: "default (unnamed) package",
			path: filepath.Join("repo", "src", "main", "java", "Standalone.java"),
			want: "Standalone",
		},
		{
			name: "falls back to simple name outside any recognized source root",
			path: filepath.Join("repo", "scripts", "Tool.java"),
			want: "Tool",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fqcnFromPath(tt.path); got != tt.want {
				t.Errorf("fqcnFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
