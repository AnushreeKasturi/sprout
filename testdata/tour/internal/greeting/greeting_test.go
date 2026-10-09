package greeting

import "testing"

func TestHello(t *testing.T) {
	if Hello() != "hello" {
		t.Fatal("wrong greeting")
	}
}
