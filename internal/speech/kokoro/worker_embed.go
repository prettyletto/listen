package kokoro

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/prettyletto/listen/internal/atomicfile"
)

//go:embed worker.py
var embeddedWorker []byte

func installEmbeddedWorker(ctx context.Context, cacheRoot string) (string, error) {
	if cacheRoot == "" {
		return "", fmt.Errorf("Kokoro worker cache directory is empty")
	}
	directory := filepath.Join(cacheRoot, "worker")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create embedded worker directory: %w", err)
	}
	path := filepath.Join(directory, "kokoro-worker-v1.py")
	if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, embeddedWorker) {
		return path, nil
	}
	if err := atomicfile.Write(ctx, path, embeddedWorker, 0o600); err != nil {
		return "", fmt.Errorf("install embedded Kokoro worker: %w", err)
	}
	return path, nil
}
