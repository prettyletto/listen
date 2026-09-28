package translate

import (
	"context"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/segment"
)

func TestCachePersistsTranslationAndBoundedContextArtifact(t *testing.T) {
	store, err := cache.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	translationCache, err := NewCache(store)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := NewContextArtifact("document-1", "en", "pt-br")
	if err != nil {
		t.Fatal(err)
	}
	artifact.Glossary["Listen"] = "Ouça"
	artifact.Summary = "A reader named Ana is learning about local history."
	fingerprint, err := translationCache.SaveContext(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(fingerprint) != 64 {
		t.Fatalf("context fingerprint length = %d, want SHA-256 hex", len(fingerprint))
	}
	contextKey := cache.Key("translation-context-artifact-v1", artifact.DocumentID, fingerprint)
	var savedContext ContextArtifact
	found, err := store.Get(context.Background(), cache.StageTranslation, contextKey, &savedContext)
	if err != nil {
		t.Fatal(err)
	}
	if !found || savedContext.TargetLanguage != "pt-BR" || savedContext.Glossary["Listen"] != "Ouça" {
		t.Fatalf("context artifact was not persisted: found=%v artifact=%#v", found, savedContext)
	}
	item := segment.Segment{ID: "segment-a", SourceText: "Listen to Ana.", SourceLang: "en"}
	key, err := CacheKey("fake-v1", item, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := translationCache.Store(context.Background(), key, Result{SegmentID: item.ID, Text: "Ouça Ana."}); err != nil {
		t.Fatal(err)
	}
	got, found, err := translationCache.Lookup(context.Background(), key)
	if err != nil || !found || got.Text != "Ouça Ana." {
		t.Fatalf("translation cache result=%#v found=%v err=%v", got, found, err)
	}
}

func TestCacheKeyIncludesSourceLocaleTargetBackendGlossaryAndContext(t *testing.T) {
	artifact, err := NewContextArtifact("document-1", "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	item := segment.Segment{ID: "segment-a", SourceText: "Read this.", SourceLang: "en"}
	base, err := CacheKey("model-a-v1", item, artifact)
	if err != nil {
		t.Fatal(err)
	}
	variants := []struct {
		name    string
		item    segment.Segment
		backend string
		context ContextArtifact
	}{
		{name: "source text", item: segment.Segment{ID: item.ID, SourceText: "Read that.", SourceLang: item.SourceLang}, backend: "model-a-v1", context: artifact},
		{name: "source language", item: segment.Segment{ID: item.ID, SourceText: item.SourceText, SourceLang: "fr"}, backend: "model-a-v1", context: artifact},
		{name: "target locale", item: item, backend: "model-a-v1", context: mustContext(t, "document-1", "en", "es-MX")},
		{name: "backend model", item: item, backend: "model-b-v1", context: artifact},
		{name: "glossary", item: item, backend: "model-a-v1", context: withGlossary(t, artifact, "Read", "Ler")},
		{name: "rolling context", item: item, backend: "model-a-v1", context: withSummary(t, artifact, "This book explains system design.")},
		{name: "prompt version", item: item, backend: "model-a-v1", context: withPromptVersion(t, artifact, "translation-prompt-v2")},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			key, err := CacheKey(variant.backend, variant.item, variant.context)
			if err != nil {
				t.Fatal(err)
			}
			if key == base {
				t.Fatal("different translation identity reused the base cache key")
			}
		})
	}
	otherID := item
	otherID.ID = "different-stable-segment-id"
	sameTextKey, err := CacheKey("model-a-v1", otherID, mustContext(t, "document-1", "en", "pt-BR"))
	if err != nil || sameTextKey != base {
		t.Fatalf("identical source text/context did not share cache identity: key=%q err=%v", sameTextKey, err)
	}
}

func TestResolveCachedPlanRequiresEverySegment(t *testing.T) {
	store, err := cache.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	translationCache, err := NewCache(store)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := NewContextArtifact("doc", "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	plan := segment.Plan{DocumentID: "doc", Segments: []segment.Segment{
		{ID: "a", Seq: 4, SourceText: "First.", SpokenText: "First.", SourceLang: "en"},
		{ID: "b", Seq: 5, SourceText: "Second.", SpokenText: "Second.", SourceLang: "en"},
	}}
	key, err := CacheKey("fake-v1", plan.Segments[0], artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := translationCache.Store(context.Background(), key, Result{Text: "Primeiro."}); err != nil {
		t.Fatal(err)
	}
	resolved, complete, err := translationCache.ResolveCachedPlan(context.Background(), plan, "fake-v1", artifact)
	if err != nil || complete || resolved.Segments[0].SpokenText != "First." {
		t.Fatalf("partial cache resolved=%#v complete=%v err=%v; expected original plan", resolved, complete, err)
	}
	key, err = CacheKey("fake-v1", plan.Segments[1], artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := translationCache.Store(context.Background(), key, Result{Text: "Segundo."}); err != nil {
		t.Fatal(err)
	}
	resolved, complete, err = translationCache.ResolveCachedPlan(context.Background(), plan, "fake-v1", artifact)
	if err != nil || !complete || resolved.Segments[0].SpokenText != "Primeiro." || resolved.Segments[1].TargetLang != "pt-BR" {
		t.Fatalf("complete cache resolved=%#v complete=%v err=%v", resolved, complete, err)
	}
}

func TestNormalizeLanguageTagCanonicalizesAndRejectsMalformedTags(t *testing.T) {
	got, err := NormalizeLanguageTag("pt-br")
	if err != nil || got != "pt-BR" {
		t.Fatalf("normalized locale=%q err=%v", got, err)
	}
	for _, invalid := range []string{"", "pt--BR", "not a locale"} {
		if _, err := NormalizeLanguageTag(invalid); err == nil {
			t.Errorf("accepted invalid language tag %q", invalid)
		}
	}
}

func TestContextArtifactBoundsSummaryAndGlossary(t *testing.T) {
	artifact, err := NewContextArtifact("doc", "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	artifact.Summary = strings.Repeat("x", maxSummaryRunes+1)
	if _, err := artifact.Fingerprint(); err == nil {
		t.Fatal("accepted an unbounded rolling summary")
	}
	artifact.Summary = ""
	artifact.Glossary["term"] = strings.Repeat("x", maxGlossaryRunes+1)
	if _, err := artifact.Fingerprint(); err == nil {
		t.Fatal("accepted an unbounded glossary value")
	}
}

func TestNormalizeTranslationTextRejectsInvalidOrOversizedOutput(t *testing.T) {
	if _, err := NormalizeTranslationText(string([]byte{0xff})); err == nil {
		t.Fatal("accepted invalid UTF-8 translation output")
	}
	if _, err := NormalizeTranslationText(strings.Repeat("x", MaxTranslationBytes+1)); err == nil {
		t.Fatal("accepted oversized translation output")
	}
	if got, err := NormalizeTranslationText("  traduzido  "); err != nil || got != "traduzido" {
		t.Fatalf("normalized translation=%q err=%v", got, err)
	}
}

func mustContext(t *testing.T, document, source, target string) ContextArtifact {
	t.Helper()
	artifact, err := NewContextArtifact(document, source, target)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func withGlossary(t *testing.T, artifact ContextArtifact, source, target string) ContextArtifact {
	t.Helper()
	artifact.Glossary[source] = target
	return artifact
}

func withSummary(t *testing.T, artifact ContextArtifact, summary string) ContextArtifact {
	t.Helper()
	artifact.Summary = summary
	return artifact
}

func withPromptVersion(t *testing.T, artifact ContextArtifact, version string) ContextArtifact {
	t.Helper()
	artifact.PromptVersion = version
	return artifact
}
