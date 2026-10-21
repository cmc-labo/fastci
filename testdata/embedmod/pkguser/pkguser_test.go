package pkguser

import "testing"

func TestRun(t *testing.T) {
	if Run() != "hello via user" {
		t.Fatal("unexpected")
	}
}
