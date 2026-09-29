package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/source"
	gocodesource "github.com/prettyletto/listen/internal/source/gocode"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

const (
	documentManifestVersion = "document-ir-v1"
	planManifestVersion     = "semantic-v2"
)

func parseCachedDocument(ctx context.Context, input source.Input, store *cache.FileStore, codeMode bool) (*domain.Document, error) {
	key, reusable, err := documentCacheKey(ctx, input, codeMode)
	if err != nil {
		return nil, err
	}
	if reusable {
		var cached domain.Document
		found, err := store.Get(ctx, cache.StageStructure, key, &cached)
		if err != nil {
			return nil, fmt.Errorf("read structure manifest: %w", err)
		}
		if found {
			return &cached, nil
		}
	}
	doc, err := parseDocumentWithCodeMode(ctx, input, codeMode)
	if err != nil {
		return nil, err
	}
	if !reusable {
		key = cache.Key(documentManifestVersion, doc.ID, input.Path)
	}
	if err := store.PutAtomic(ctx, cache.StageStructure, key, doc); err != nil {
		return nil, fmt.Errorf("save structure manifest: %w", err)
	}
	return doc, nil
}

func documentCacheKey(ctx context.Context, input source.Input, codeMode bool) (string, bool, error) {
	if input.Path == "" || input.Path == "stdin" || input.R != nil {
		return "", false, nil
	}
	path, err := filepath.Abs(input.Path)
	if err != nil {
		return "", false, fmt.Errorf("resolve source for structure cache: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false, fmt.Errorf("inspect source for structure cache: %w", err)
	}
	if info.IsDir() {
		// Directory identity depends on its current ignore rules and file set.
		// Parse it on each run rather than reusing a manifest keyed by one path.
		return "", false, nil
	}
	if codeMode {
		sourceName := input.Name
		if sourceName == "" {
			sourceName = input.Path
		}
		if !strings.EqualFold(filepath.Ext(sourceName), ".go") {
			return "", false, gocodesource.ErrNotGoSource
		}
		if info.Size() > gocodesource.MaxSourceBytes {
			return "", false, gocodesource.ErrSourceTooLarge
		}
	}
	digest, err := fingerprintFile(ctx, path)
	if err != nil {
		return "", false, fmt.Errorf("fingerprint source: %w", err)
	}
	if codeMode {
		return cache.Key(documentManifestVersion, gocodesource.ParserVersion, "go-code", path, strings.ToLower(filepath.Ext(input.Name)), digest), true, nil
	}
	return cache.Key(documentManifestVersion, path, strings.ToLower(filepath.Ext(input.Name)), digest), true, nil
}

func fingerprintFile(ctx context.Context, path string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("source fingerprint requires a context")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, reader: file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func planCachedSegments(ctx context.Context, doc *domain.Document, sourcePath string, store *cache.FileStore) (segment.Plan, error) {
	if doc == nil {
		return segment.Plan{}, fmt.Errorf("cannot cache a segment plan for a nil document")
	}
	sourceIdentity, err := planSourceIdentity(sourcePath)
	if err != nil {
		return segment.Plan{}, err
	}
	key := cache.Key(planManifestVersion, doc.ID, sourceIdentity, "target-70", "maximum-150", "include-code-false")
	var plan segment.Plan
	found, err := store.Get(ctx, cache.StageSegments, key, &plan)
	if err != nil {
		return segment.Plan{}, fmt.Errorf("read segment manifest: %w", err)
	}
	if found {
		if plan.DocumentID != doc.ID {
			return segment.Plan{}, fmt.Errorf("segment manifest belongs to a different document")
		}
		return plan, nil
	}
	plan, err = segment.PlanDocument(doc, segment.Options{})
	if err != nil {
		return segment.Plan{}, err
	}
	if err := store.PutAtomic(ctx, cache.StageSegments, key, plan); err != nil {
		return segment.Plan{}, fmt.Errorf("save segment manifest: %w", err)
	}
	return plan, nil
}

func planSourceIdentity(sourcePath string) (string, error) {
	if sourcePath == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", fmt.Errorf("resolve source for segment cache: %w", err)
	}
	if info, err := os.Stat(absolute); err == nil && info.IsDir() {
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return "", fmt.Errorf("resolve documentation directory for segment cache: %w", err)
		}
		return resolved, nil
	}
	return absolute, nil
}

func allAudioCached(ctx context.Context, store *speech.Cache, plan segment.Plan, voice string, speed float64) (bool, error) {
	if store == nil || len(plan.Segments) == 0 {
		return false, nil
	}
	for _, item := range plan.Segments {
		key, err := speech.CacheKey(kokoro.EngineID, speech.Request{Segment: item, Voice: voice, Speed: speed})
		if err != nil {
			return false, err
		}
		if _, found, err := store.Lookup(ctx, key); err != nil {
			return false, err
		} else if !found {
			return false, nil
		}
	}
	return true, nil
}

type cachedAudioSynthesizer struct{}

func (cachedAudioSynthesizer) ID() string { return kokoro.EngineID }

func (cachedAudioSynthesizer) Synthesize(ctx context.Context, request speech.Request) (speech.Result, error) {
	if err := ctx.Err(); err != nil {
		return speech.Result{}, err
	}
	return speech.Result{}, fmt.Errorf("cached audio for segment %d became unavailable", request.Segment.Seq)
}

func (cachedAudioSynthesizer) Close() error { return nil }

func defaultExportDirectory(sourceOption, sourcePath string) string {
	if sourceOption != "-" {
		if absolute, err := filepath.Abs(sourcePath); err == nil {
			stem := strings.TrimSuffix(filepath.Base(absolute), filepath.Ext(absolute))
			return filepath.Join(filepath.Dir(absolute), safePathComponent(stem)+"-audio")
		}
	}
	return filepath.Join(".", "audiobook-audio")
}

func safePathComponent(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			builder.WriteRune(char)
			lastDash = false
			continue
		}
		if builder.Len() > 0 && !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	component := strings.Trim(builder.String(), "-")
	if component == "" {
		return "audiobook"
	}
	if runes := []rune(component); len(runes) > 60 {
		component = strings.Trim(string(runes[:60]), "-")
	}
	return component
}
