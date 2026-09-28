package speech

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/prettyletto/listen/internal/cache"
)

const speechCacheVersion = "speech-v1"

type Cache struct {
	root     string
	metadata cache.Store
}

func NewCache(root string) (*Cache, error) {
	store, err := cache.NewFileStore(root)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve speech cache: %w", err)
	}
	return &Cache{root: absolute, metadata: store}, nil
}

func CacheKey(engineID string, request Request) (string, error) {
	if engineID == "" {
		return "", errors.New("speech engine ID is empty")
	}
	text := request.Segment.SpokenText
	if text == "" {
		text = request.Segment.SourceText
	}
	data, err := json.Marshal(struct {
		Version string
		Engine  string
		Text    string
		Voice   string
		Speed   float64
	}{speechCacheVersion, engineID, text, request.Voice, request.Speed})
	if err != nil {
		return "", fmt.Errorf("build speech cache key: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (c *Cache) OutputPath(key string) (string, error) {
	if c == nil || c.root == "" {
		return "", errors.New("speech cache is not initialized")
	}
	if key == "" {
		return "", errors.New("speech cache key is empty")
	}
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(c.root, "speech", hex.EncodeToString(digest[:])+".wav"), nil
}

func (c *Cache) Lookup(ctx context.Context, key string) (Result, bool, error) {
	var result Result
	found, err := c.metadata.Get(ctx, cache.StageSpeech, key, &result)
	if err != nil || !found {
		return Result{}, false, err
	}
	expectedPath, err := c.OutputPath(key)
	if err != nil {
		return Result{}, false, err
	}
	if filepath.Clean(result.Path) != filepath.Clean(expectedPath) {
		return Result{}, false, errors.New("speech cache metadata points outside its generated audio path")
	}
	duration, err := ValidateWAV(result.Path)
	if err != nil {
		return Result{}, false, nil
	}
	result.Duration = duration
	return result, true, nil
}

func (c *Cache) Store(ctx context.Context, key string, result Result) error {
	expectedPath, err := c.OutputPath(key)
	if err != nil {
		return err
	}
	if filepath.Clean(result.Path) != filepath.Clean(expectedPath) {
		return errors.New("synthesizer returned a path outside the speech cache")
	}
	duration, err := ValidateWAV(result.Path)
	if err != nil {
		return fmt.Errorf("validate synthesized audio: %w", err)
	}
	result.Duration = duration
	return c.metadata.PutAtomic(ctx, cache.StageSpeech, key, result)
}

func (c *Cache) PrepareOutput(key string) (string, error) {
	path, err := c.OutputPath(key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create speech cache directory: %w", err)
	}
	return path, nil
}
