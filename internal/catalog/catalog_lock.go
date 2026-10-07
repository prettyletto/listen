package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type catalogLock struct {
	file *os.File
}

func acquireLock(ctx context.Context, path string) (*catalogLock, error) {
	if ctx == nil {
		return nil, errors.New("catalog lock requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create catalog directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open catalog lock: %w", err)
	}
	for {
		locked, lockErr := tryFileLock(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire catalog lock: %w", lockErr)
		}
		if locked {
			return &catalogLock{file: file}, nil
		}
		timer := time.NewTimer(15 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (lock *catalogLock) close() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = releaseFileLock(lock.file)
	_ = lock.file.Close()
}
