package pipeline

import "testing"

func TestReorderBufferCommitsInOrder(t *testing.T) {
	r := NewReorderBuffer[string](0)
	if ready, err := r.Push(2, "two"); err != nil || len(ready) != 0 {
		t.Fatalf("push 2: %v %#v", err, ready)
	}
	if ready, err := r.Push(0, "zero"); err != nil || len(ready) != 1 || ready[0] != "zero" {
		t.Fatalf("push 0: %v %#v", err, ready)
	}
	ready, err := r.Push(1, "one")
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 2 || ready[0] != "one" || ready[1] != "two" {
		t.Fatalf("unexpected ready: %#v", ready)
	}
}
