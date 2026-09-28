package apperr

import (
	"errors"
	"testing"
)

func TestErrorPreservesCauseAndCode(t *testing.T) {
	cause := errors.New("private detail")
	err := New("source_unavailable", "Could not read the source.", "Check that the file exists and is readable.", cause)
	if !errors.Is(err, cause) {
		t.Fatal("cause was not preserved")
	}
	if !HasCode(err, "source_unavailable") {
		t.Fatal("stable code was not found")
	}
	if got := err.Error(); got != "Could not read the source." {
		t.Fatalf("default error text exposed or lost details: %q", got)
	}
}
