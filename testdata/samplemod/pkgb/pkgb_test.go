package pkgb

import "testing"

func TestGreet(t *testing.T) {
	if Greet() != "hello from c via b" {
		t.Fatal("unexpected")
	}
}
