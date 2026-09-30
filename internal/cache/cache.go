package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/prettyletto/listen/internal/atomicfile"
	"github.com/prettyletto/listen/internal/paths"
)

type Stage string

const (
	StageStructure   Stage = "structure"
	StageSegments    Stage = "segments"
	StageOCR         Stage = "ocr"
	StageTranslation Stage = "translation"
	StageSpeech      Stage = "speech"
	StageSession     Stage = "sessions"
)

// Key derives a stable opaque key from ordered identity components. It is
// useful for manifests whose identity includes several versioned inputs.
func Key(parts ...string) string {
	data, _ := json.Marshal(parts)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

type Store interface {
	Get(context.Context, Stage, string, any) (bool, error)
	PutAtomic(context.Context, Stage, string, any) error
}

// FileStore stores each stage artifact as an atomic JSON file. Keys are hashed
// before they become path components so callers can use content-derived keys
// without needing to sanitize them first.
type FileStore struct {
	root string
}

func NewFileStore(root string) (*FileStore, error) {
	if root == "" {
		return nil, errors.New("cache root is empty")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve cache root: %w", err)
	}
	return &FileStore{root: root}, nil
}

// NewDefaultStore uses the user's XDG cache directory.
func NewDefaultStore() (*FileStore, error) {
	userPaths, err := paths.FromEnv()
	if err != nil {
		return nil, err
	}
	return NewFileStore(userPaths.CacheDir)
}

func (s *FileStore) artifactPath(stage Stage, key string) (string, error) {
	if s == nil || s.root == "" {
		return "", errors.New("cache store is not initialized")
	}
	switch stage {
	case StageStructure, StageSegments, StageOCR, StageTranslation, StageSpeech, StageSession:
	default:
		return "", fmt.Errorf("unsupported cache stage %q", stage)
	}
	if key == "" {
		return "", errors.New("cache key is empty")
	}
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(s.root, string(stage), hex.EncodeToString(digest[:])+".json"), nil
}

func (s *FileStore) Get(ctx context.Context, stage Stage, key string, target any) (bool, error) {
	if ctx == nil {
		return false, errors.New("cache read requires a context")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if target == nil {
		return false, errors.New("cache target is nil")
	}
	path, err := s.artifactPath(stage, key)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read cache artifact: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("decode cache artifact: %w", err)
	}
	return true, nil
}

func (s *FileStore) PutAtomic(ctx context.Context, stage Stage, key string, value any) error {
	if ctx == nil {
		return errors.New("cache write requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.artifactPath(stage, key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode cache artifact: %w", err)
	}
	return atomicfile.Write(ctx, path, data, 0o600)
}
