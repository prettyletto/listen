package atomicfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteReplacesDestinationAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "data.json")
	if err := Write(context.Background(), path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(context.Background(), path, []byte("second"), 0o640); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("got %q, want second", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestWriteCancellationPreservesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Write(ctx, path, []byte("partial"), 0o600)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "safe" {
		t.Fatalf("existing file changed to %q", data)
	}
}

func TestWriteRemovesTemporaryFileOnCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Write(ctx, filepath.Join(dir, "value"), []byte("discard"), 0o600); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("left temporary entries behind: %s", strings.Join([]string{entries[0].Name()}, ", "))
	}
}
