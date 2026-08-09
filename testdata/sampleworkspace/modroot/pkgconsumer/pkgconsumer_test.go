package pkgconsumer

import "testing"

func TestGreet(t *testing.T) {
	if Greet() != "hello from leaf via consumer" {
		t.Fatal("unexpected")
	}
}
