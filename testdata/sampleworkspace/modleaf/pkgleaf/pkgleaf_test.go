package pkgleaf

import "testing"

func TestHello(t *testing.T) {
	if Hello() != "hello from leaf" {
		t.Fatal("unexpected")
	}
}
