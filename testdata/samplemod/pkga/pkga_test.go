package pkga

import "testing"

func TestRun(t *testing.T) {
	if Run() != "hello from c via b via a" {
		t.Fatal("unexpected")
	}
}
