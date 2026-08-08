package pkge_test

import (
	"testing"

	"samplemod/pkgd"
	"samplemod/pkge"
)

func TestBar(t *testing.T) {
	if pkge.Bar() != "bar" {
		t.Fatal("unexpected")
	}
	// Only referenced from a test file: pkgd is a test-only dependency of
	// pkge, used here to exercise import edges that exist solely in _test.go
	// files.
	_ = pkgd.Foo()
}
