package translate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/segment"
	"golang.org/x/text/language"
)

const (
	CacheVersion           = "translation-v1"
	ContextArtifactVersion = 1
	PromptVersion          = "translation-prompt-v1"
	ContextPolicyVersion   = "bounded-context-v1"
	maxGlossaryEntries     = 128
	maxGlossaryRunes       = 256
	maxSummaryRunes        = 4000
	maxStyleRunes          = 1000
)

// ContextArtifact is the bounded, versioned translation context shared by
// segments in a document session. Its fingerprint participates in each
// translation cache key.
type ContextArtifact struct {
	Version          int               `json:"version"`
	DocumentID       string            `json:"document_id"`
	SourceLanguage   string            `json:"source_language,omitempty"`
	TargetLanguage   string            `json:"target_language"`
	PromptVersion    string            `json:"prompt_version"`
	PolicyVersion    string            `json:"policy_version"`
	StyleInstruction string            `json:"style_instruction,omitempty"`
	Glossary         map[string]string `json:"glossary,omitempty"`
	Summary          string            `json:"summary,omitempty"`
}

func NewContextArtifact(documentID, sourceLanguage, targetLanguage string) (ContextArtifact, error) {
	if strings.TrimSpace(documentID) == "" {
		return ContextArtifact{}, errors.New("translation context requires a document ID")
	}
	target, err := NormalizeLanguageTag(targetLanguage)
	if err != nil {
		return ContextArtifact{}, err
	}
	return ContextArtifact{
		Version:          ContextArtifactVersion,
		DocumentID:       documentID,
		SourceLanguage:   normalizeOptionalLanguageTag(sourceLanguage),
		TargetLanguage:   target,
		PromptVersion:    PromptVersion,
		PolicyVersion:    ContextPolicyVersion,
		StyleInstruction: "Preserve meaning, tone, names, and paragraph boundaries.",
		Glossary:         map[string]string{},
	}, nil
}

// NormalizeLanguageTag validates and canonicalizes a BCP-47 language tag.
func NormalizeLanguageTag(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("target language is empty")
	}
	tag, err := language.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid BCP-47 language tag %q: %w", value, err)
	}
	return tag.String(), nil
}

func normalizeOptionalLanguageTag(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if tag, err := language.Parse(value); err == nil {
		return tag.String()
	}
	return value
}

func (artifact ContextArtifact) normalized() (ContextArtifact, error) {
	if artifact.Version != ContextArtifactVersion {
		return ContextArtifact{}, fmt.Errorf("unsupported translation context version %d", artifact.Version)
	}
	if strings.TrimSpace(artifact.DocumentID) == "" {
		return ContextArtifact{}, errors.New("translation context requires a document ID")
	}
	target, err := NormalizeLanguageTag(artifact.TargetLanguage)
	if err != nil {
		return ContextArtifact{}, err
	}
	artifact.DocumentID = strings.TrimSpace(artifact.DocumentID)
	artifact.SourceLanguage = normalizeOptionalLanguageTag(artifact.SourceLanguage)
	artifact.TargetLanguage = target
	artifact.PromptVersion = strings.TrimSpace(artifact.PromptVersion)
	if artifact.PromptVersion == "" {
		return ContextArtifact{}, errors.New("translation context requires a prompt version")
	}
	artifact.PolicyVersion = strings.TrimSpace(artifact.PolicyVersion)
	if artifact.PolicyVersion == "" {
		return ContextArtifact{}, errors.New("translation context requires a policy version")
	}
	artifact.StyleInstruction = strings.TrimSpace(artifact.StyleInstruction)
	artifact.Summary = strings.TrimSpace(artifact.Summary)
	if utf8.RuneCountInString(artifact.StyleInstruction) > maxStyleRunes {
		return ContextArtifact{}, fmt.Errorf("translation style instruction exceeds %d characters", maxStyleRunes)
	}
	if utf8.RuneCountInString(artifact.Summary) > maxSummaryRunes {
		return ContextArtifact{}, fmt.Errorf("translation summary exceeds %d characters", maxSummaryRunes)
	}
	if len(artifact.Glossary) > maxGlossaryEntries {
		return ContextArtifact{}, fmt.Errorf("translation glossary exceeds %d entries", maxGlossaryEntries)
	}
	glossary := make(map[string]string, len(artifact.Glossary))
	for source, translation := range artifact.Glossary {
		source, translation = strings.TrimSpace(source), strings.TrimSpace(translation)
		if source == "" || translation == "" {
			return ContextArtifact{}, errors.New("translation glossary terms and translations must be non-empty")
		}
		if utf8.RuneCountInString(source) > maxGlossaryRunes || utf8.RuneCountInString(translation) > maxGlossaryRunes {
			return ContextArtifact{}, fmt.Errorf("translation glossary terms must not exceed %d characters", maxGlossaryRunes)
		}
		if _, exists := glossary[source]; exists {
			return ContextArtifact{}, fmt.Errorf("translation glossary repeats the term %q", source)
		}
		glossary[source] = translation
	}
	artifact.Glossary = glossary
	return artifact, nil
}

// Fingerprint identifies the content and policy of the bounded context. The
// document ID is omitted so identical context can be reused across documents.
func (artifact ContextArtifact) Fingerprint() (string, error) {
	artifact, err := artifact.normalized()
	if err != nil {
		return "", err
	}
	identity := struct {
		Version          int
		SourceLanguage   string
		TargetLanguage   string
		PromptVersion    string
		PolicyVersion    string
		StyleInstruction string
		Glossary         map[string]string
		Summary          string
	}{
		artifact.Version, artifact.SourceLanguage, artifact.TargetLanguage,
		artifact.PromptVersion, artifact.PolicyVersion, artifact.StyleInstruction,
		artifact.Glossary, artifact.Summary,
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode translation context identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

type Cache struct {
	store cache.Store
}

func NewCache(store cache.Store) (*Cache, error) {
	if store == nil {
		return nil, errors.New("translation cache requires a store")
	}
	return &Cache{store: store}, nil
}

type cachedTranslation struct {
	Version int    `json:"version"`
	Text    string `json:"text"`
}

func (c *Cache) Lookup(ctx context.Context, key string) (Result, bool, error) {
	if c == nil || c.store == nil {
		return Result{}, false, errors.New("translation cache is not initialized")
	}
	var entry cachedTranslation
	found, err := c.store.Get(ctx, cache.StageTranslation, key, &entry)
	if err != nil || !found {
		return Result{}, found, err
	}
	if entry.Version != 1 {
		return Result{}, false, errors.New("translation cache entry is invalid")
	}
	text, err := NormalizeTranslationText(entry.Text)
	if err != nil {
		return Result{}, false, fmt.Errorf("translation cache entry is invalid: %w", err)
	}
	return Result{Text: text}, true, nil
}

func (c *Cache) Store(ctx context.Context, key string, result Result) error {
	if c == nil || c.store == nil {
		return errors.New("translation cache is not initialized")
	}
	text, err := NormalizeTranslationText(result.Text)
	if err != nil {
		return err
	}
	return c.store.PutAtomic(ctx, cache.StageTranslation, key, cachedTranslation{Version: 1, Text: text})
}

// SaveContext persists a content-addressed context artifact beside translation
// results and returns the key used for its atomic cache entry.
func (c *Cache) SaveContext(ctx context.Context, artifact ContextArtifact) (string, error) {
	if c == nil || c.store == nil {
		return "", errors.New("translation cache is not initialized")
	}
	artifact, err := artifact.normalized()
	if err != nil {
		return "", err
	}
	fingerprint, err := artifact.Fingerprint()
	if err != nil {
		return "", err
	}
	key := cache.Key("translation-context-artifact-v1", artifact.DocumentID, fingerprint)
	if err := c.store.PutAtomic(ctx, cache.StageTranslation, key, artifact); err != nil {
		return "", fmt.Errorf("save translation context artifact: %w", err)
	}
	return fingerprint, nil
}

func CacheKey(translatorID string, item segment.Segment, artifact ContextArtifact) (string, error) {
	if strings.TrimSpace(translatorID) == "" {
		return "", errors.New("translation backend ID is empty")
	}
	if strings.TrimSpace(item.SourceText) == "" {
		return "", errors.New("source segment text is empty")
	}
	artifact, err := artifact.normalized()
	if err != nil {
		return "", err
	}
	if item.TargetLang != "" {
		target, err := NormalizeLanguageTag(item.TargetLang)
		if err != nil {
			return "", err
		}
		if target != artifact.TargetLanguage {
			return "", errors.New("segment target language does not match translation context")
		}
	}
	contextFingerprint, err := artifact.Fingerprint()
	if err != nil {
		return "", err
	}
	sourceText := sha256.Sum256([]byte(item.SourceText))
	sourceLanguage := normalizeOptionalLanguageTag(item.SourceLang)
	if sourceLanguage == "" {
		sourceLanguage = artifact.SourceLanguage
	}
	identity := struct {
		Version            string
		SourceTextHash     string
		SourceLanguage     string
		TargetLanguage     string
		TranslatorID       string
		PromptVersion      string
		ContextPolicy      string
		ContextFingerprint string
	}{
		CacheVersion, hex.EncodeToString(sourceText[:]), sourceLanguage,
		artifact.TargetLanguage, strings.TrimSpace(translatorID), artifact.PromptVersion,
		artifact.PolicyVersion, contextFingerprint,
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode translation cache identity: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// ResolveCachedPlan returns a translated plan only when every requested
// segment is present. A partial hit leaves the original plan intact so the
// streaming path can combine cached and newly translated segments.
func (c *Cache) ResolveCachedPlan(ctx context.Context, plan segment.Plan, translatorID string, artifact ContextArtifact) (segment.Plan, bool, error) {
	resolved := segment.Plan{DocumentID: plan.DocumentID, Segments: append([]segment.Segment(nil), plan.Segments...)}
	for index, item := range plan.Segments {
		key, err := CacheKey(translatorID, item, artifact)
		if err != nil {
			return segment.Plan{}, false, err
		}
		result, found, err := c.Lookup(ctx, key)
		if err != nil {
			return segment.Plan{}, false, err
		}
		if !found {
			return plan, false, nil
		}
		resolved.Segments[index].SpokenText = result.Text
		resolved.Segments[index].TargetLang = artifact.TargetLanguage
	}
	return resolved, true, nil
}
