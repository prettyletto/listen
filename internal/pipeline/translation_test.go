package pipeline

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/testkit"
	"github.com/prettyletto/listen/internal/translate"
)

func TestStreamTranslatesAndCommitsOutOfOrderStagesInSourceOrder(t *testing.T) {
	plan := translatedTestPlan("translation-document", 4)
	store, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	_ = store
	translator := &testkit.FakeTranslator{
		DelayByID: map[string]time.Duration{
			"segment-0": 80 * time.Millisecond,
			"segment-1": 5 * time.Millisecond,
			"segment-2": 10 * time.Millisecond,
			"segment-3": 2 * time.Millisecond,
		},
		TranslateText: func(item segment.Segment, target string) string {
			return target + ": " + item.SourceText
		},
	}
	synth := &testkit.FakeSynthesizer{
		DelayBySeq: map[uint64]time.Duration{0: 50 * time.Millisecond, 1: time.Millisecond, 2: 8 * time.Millisecond, 3: 2 * time.Millisecond},
		Duration:   time.Second,
	}
	player := &recordingPlayer{}
	report, err := Stream(context.Background(), plan, synth, &memorySpeechCache{results: make(map[string]speech.Result)}, player, StreamOptions{
		Workers: 3, QueueCapacity: 4, StartupBuffer: 2 * time.Second,
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: "pt-BR",
		TranslationWorkers: 4, TranslationBatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSeq := []uint64{0, 1, 2, 3}
	gotSeq := make([]uint64, len(player.items))
	for index, item := range player.items {
		gotSeq[index] = item.Seq
	}
	if !reflect.DeepEqual(gotSeq, wantSeq) {
		t.Fatalf("playback order = %v, want %v", gotSeq, wantSeq)
	}
	completed := translator.CompletionSnapshot()
	if len(completed) != 4 || completed[0] == "segment-0" {
		t.Fatalf("fake translations did not complete out of order: %v", completed)
	}
	if report.TranslationGenerated != 4 || report.TranslationReused != 0 || report.Generated != 4 {
		t.Fatalf("unexpected translation/speech report: %#v", report)
	}
	seenSpeech := make(map[string]speech.Request)
	for _, request := range synth.RequestSnapshot() {
		seenSpeech[request.Segment.ID] = request
	}
	for _, item := range plan.Segments {
		request, ok := seenSpeech[item.ID]
		if !ok || request.Segment.TargetLang != "pt-BR" || request.Segment.SpokenText != "pt-BR: "+item.SourceText {
			t.Fatalf("speech did not receive translated target-locale segment: %#v", request)
		}
	}
	for _, request := range translator.RequestSnapshot() {
		if request.TargetLanguage != "pt-BR" || request.PromptVersion != artifact.PromptVersion || request.Glossary["Listen"] != "Ouça" || request.ContextHint != artifact.Summary {
			t.Fatalf("translator request lost target/context artifact: %#v", request)
		}
	}
}

func TestProbeTranslationCachesOpeningSampleForOrderedStream(t *testing.T) {
	plan := translatedTestPlan("probe-document", 4)
	_, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	translator := &testkit.FakeTranslator{}
	probe, err := ProbeTranslation(context.Background(), plan.Segments, translator, translationCache, artifact, 2)
	if err != nil {
		t.Fatal(err)
	}
	if probe.WallTime <= 0 || len(probe.Translated) != 2 || probe.Translated[0].ID != "segment-0" || probe.Translated[1].ID != "segment-1" {
		t.Fatalf("translation probe = %#v", probe)
	}
	if translator.CallCount() != 1 {
		t.Fatalf("probe made %d backend calls, want 1", translator.CallCount())
	}
	report, err := Stream(context.Background(), plan, &testkit.FakeSynthesizer{Duration: time.Second}, nil, nil, StreamOptions{
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
		TranslationWorkers: 1, TranslationBatchSize: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.TranslationGenerated != 2 || report.TranslationReused != 2 || translator.CallCount() != 2 {
		t.Fatalf("probe cache was not reused by the stream: report=%#v calls=%d", report, translator.CallCount())
	}
}

func TestStreamEmitsStageAndPlaybackBufferMeasurements(t *testing.T) {
	plan := translatedTestPlan("measurement-document", 2)
	_, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	var mu sync.Mutex
	translationSamples, speechSamples, bufferSamples := 0, 0, 0
	translator := &testkit.FakeTranslator{}
	player := &recordingPlayer{}
	_, err := Stream(context.Background(), plan, &testkit.FakeSynthesizer{Duration: 20 * time.Second}, nil, player, StreamOptions{
		StartupBuffer: time.Second,
		Translator:    translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
		TranslationWorkers: 1, TranslationBatchSize: 4,
		OnTranslationMeasured: func(wall time.Duration, segments []segment.Segment) {
			if wall <= 0 || len(segments) != 2 {
				t.Errorf("translation measurement wall=%v segments=%d", wall, len(segments))
			}
			mu.Lock()
			translationSamples++
			mu.Unlock()
		},
		OnSpeechMeasured: func(_ segment.Segment, wall, audio time.Duration) {
			if wall <= 0 || audio != 20*time.Second {
				t.Errorf("speech measurement wall=%v audio=%v", wall, audio)
			}
			mu.Lock()
			speechSamples++
			mu.Unlock()
		},
		OnBufferHealth: func(seconds float64, at time.Time) {
			if seconds <= 0 || at.IsZero() {
				t.Errorf("buffer measurement seconds=%v at=%v", seconds, at)
			}
			bufferSamples++
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if translationSamples != 1 || speechSamples != 2 || bufferSamples < 2 {
		t.Fatalf("measurement counts translation=%d speech=%d buffer=%d", translationSamples, speechSamples, bufferSamples)
	}
}

func TestTranslationBatchesStayWithinMajorUnits(t *testing.T) {
	plan := segment.Plan{DocumentID: "batch-document", Segments: []segment.Segment{
		{ID: "a", Seq: 3, SourceText: "First.", SourceLang: "en", MajorUnitID: "chapter-a"},
		{ID: "b", Seq: 4, SourceText: "Second.", SourceLang: "en", MajorUnitID: "chapter-a"},
		{ID: "c", Seq: 5, SourceText: "Third.", SourceLang: "en", MajorUnitID: "chapter-b"},
	}}
	_, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	translator := &testkit.FakeTranslator{}
	_, err := Stream(context.Background(), plan, &testkit.FakeSynthesizer{}, nil, nil, StreamOptions{
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
		TranslationWorkers: 1, TranslationBatchSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := translator.RequestSnapshot()
	if len(requests) != 2 || len(requests[0].Segments) != 2 || len(requests[1].Segments) != 1 {
		t.Fatalf("translation batches crossed major-unit boundary: %#v", requests)
	}
}

func TestStreamRejectsRemoteTranslatorBeforeSendingAnyText(t *testing.T) {
	plan := translatedTestPlan("private-document", 1)
	_, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	translator := &testkit.FakeTranslator{Remote: true}
	synth := &testkit.FakeSynthesizer{}
	_, err := Stream(context.Background(), plan, synth, nil, nil, StreamOptions{
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
	})
	if err == nil || !strings.Contains(err.Error(), "explicit cloud authorization") {
		t.Fatalf("remote translation was not blocked: %v", err)
	}
	if translator.CallCount() != 0 || len(synth.RequestSnapshot()) != 0 {
		t.Fatal("a remote translator or speech worker was called before cloud policy exists")
	}
}

type forgedCloudAuthorizationTranslator struct {
	*testkit.FakeTranslator
}

func (forgedCloudAuthorizationTranslator) CloudTranslationAuthorized() bool { return true }

func TestStreamRejectsRemoteTranslatorWithForgedAuthorizationMarker(t *testing.T) {
	plan := translatedTestPlan("forged-authorization-document", 1)
	_, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	translator := forgedCloudAuthorizationTranslator{FakeTranslator: &testkit.FakeTranslator{Remote: true}}
	synth := &testkit.FakeSynthesizer{}
	_, err := Stream(context.Background(), plan, synth, nil, nil, StreamOptions{
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
	})
	if err == nil || !strings.Contains(err.Error(), "explicit cloud authorization") {
		t.Fatalf("forged remote authorization was not blocked: %v", err)
	}
	if translator.CallCount() != 0 || len(synth.RequestSnapshot()) != 0 {
		t.Fatal("a forged remote translator or speech worker was called before cloud policy exists")
	}
}

func TestStreamRejectsInvalidTranslationBeforeSpeech(t *testing.T) {
	plan := translatedTestPlan("invalid-translation-document", 1)
	_, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	translator := &testkit.FakeTranslator{
		TranslateText: func(segment.Segment, string) string { return string([]byte{0xff}) },
	}
	synth := &testkit.FakeSynthesizer{}
	_, err := Stream(context.Background(), plan, synth, nil, nil, StreamOptions{
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
	})
	if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid translation error = %v", err)
	}
	if len(synth.RequestSnapshot()) != 0 {
		t.Fatal("speech started before the translation result was validated")
	}
}

func TestStreamInterruptionReusesTranslationAndSpeechCaches(t *testing.T) {
	plan := translatedTestPlan("resume-translation-document", 3)
	_, translationCache, artifact := translationTestCache(t, plan.DocumentID)
	speechCache := &memorySpeechCache{results: make(map[string]speech.Result)}
	translator := &testkit.FakeTranslator{
		DelayByID:     map[string]time.Duration{"segment-1": 250 * time.Millisecond},
		TranslateText: func(item segment.Segment, _ string) string { return "translated: " + item.SourceText },
	}
	synth := &testkit.FakeSynthesizer{Duration: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	first, err := Stream(ctx, plan, synth, speechCache, nil, StreamOptions{
		Workers: 1, QueueCapacity: 1,
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
		TranslationWorkers: 1, TranslationBatchSize: 1,
		OnProgress: func(completed, _ int) {
			if completed == 1 {
				cancel()
			}
		},
	})
	cancel()
	if !errors.Is(err, context.Canceled) || first.Generated != 1 {
		t.Fatalf("interrupted stream report=%#v err=%v", first, err)
	}

	translator.DelayByID = nil
	second, err := Stream(context.Background(), plan, synth, speechCache, nil, StreamOptions{
		Workers: 2, QueueCapacity: 2,
		Translator: translator, TranslationCache: translationCache,
		TranslationContext: artifact, TargetLanguage: artifact.TargetLanguage,
		TranslationWorkers: 2, TranslationBatchSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.TranslationReused != 1 || second.TranslationGenerated != 2 || second.Reused != 1 || second.Generated != 2 {
		t.Fatalf("resume did not reuse both completed stage caches: %#v", second)
	}
	if len(translator.CompletionSnapshot()) != 3 || len(synth.Snapshot()) != 3 {
		t.Fatalf("resume repeated committed work: translated=%v spoken=%v", translator.CompletionSnapshot(), synth.Snapshot())
	}
}

func translatedTestPlan(documentID string, count int) segment.Plan {
	plan := segment.Plan{DocumentID: documentID, Segments: make([]segment.Segment, count)}
	for index := range plan.Segments {
		plan.Segments[index] = segment.Segment{
			ID: "segment-" + string(rune('0'+index)), Seq: uint64(index),
			SourceText: "Source sentence " + string(rune('A'+index)) + ".", SpokenText: "Source sentence " + string(rune('A'+index)) + ".",
			SourceLang: "en", MajorUnitID: "chapter-a",
		}
	}
	return plan
}

func translationTestCache(t *testing.T, documentID string) (*cache.FileStore, *translate.Cache, translate.ContextArtifact) {
	t.Helper()
	store, err := cache.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	translationCache, err := translate.NewCache(store)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := translate.NewContextArtifact(documentID, "en", "pt-BR")
	if err != nil {
		t.Fatal(err)
	}
	artifact.Glossary["Listen"] = "Ouça"
	artifact.Summary = "A reader named Ana is learning about local history."
	if _, err := translationCache.SaveContext(context.Background(), artifact); err != nil {
		t.Fatal(err)
	}
	return store, translationCache, artifact
}
