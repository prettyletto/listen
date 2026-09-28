package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/apperr"
)

func TestErrorPrintsActionableTextWithoutCause(t *testing.T) {
	var out bytes.Buffer
	output := New(&out)
	err := apperr.New("source_unavailable", "Could not read the source.", "Check the path and permissions.", errors.New("private parser detail"))
	if writeErr := output.Error(err); writeErr != nil {
		t.Fatal(writeErr)
	}
	got := out.String()
	if !strings.Contains(got, "Could not read the source.") || !strings.Contains(got, "Check the path and permissions.") {
		t.Fatalf("missing user guidance: %q", got)
	}
	if strings.Contains(got, "private parser detail") {
		t.Fatalf("default output exposed internal cause: %q", got)
	}
}
