package pkgc

import "testing"

func TestHello(t *testing.T) {
	if Hello() != "hello from c" {
		t.Fatal("unexpected")
	}
}
