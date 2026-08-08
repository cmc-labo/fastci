package pkgd

import "testing"

func TestFoo(t *testing.T) {
	if Foo() != "foo" {
		t.Fatal("unexpected")
	}
}
