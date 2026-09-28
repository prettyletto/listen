// Package atomicfile writes complete files by renaming a synced temporary file.
package atomicfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Write replaces path only after all data has been written and synced. The
// temporary file lives beside the destination so rename remains atomic.
func Write(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	if ctx == nil {
		return errors.New("atomic write requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if path == "" {
		return errors.New("atomic write path is empty")
	}
	if mode == 0 {
		mode = 0o600
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".listen-*.partial")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	temporary := file.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporary)
		}
	}()

	if err := file.Chmod(mode.Perm()); err != nil {
		_ = file.Close()
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("commit atomic file: %w", err)
	}
	committed = true
	if directory, err := os.Open(dir); err == nil {
		defer directory.Close()
		if err := directory.Sync(); err != nil {
			return fmt.Errorf("sync destination directory: %w", err)
		}
	}
	return nil
}

// WriteJSON encodes value as indented JSON before atomically replacing path.
func WriteJSON(ctx context.Context, path string, value any, mode os.FileMode) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}
	data = append(data, '\n')
	return Write(ctx, path, data, mode)
}
