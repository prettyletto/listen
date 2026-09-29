package app

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/pipeline"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/source"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

func TestDocumentAndSegmentManifestsArePersistedByVersionedIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("Chapter 1\n\nA sentence worth reading."), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewFileStore(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	input := source.Input{Name: filepath.Base(path), Path: path}
	keyBefore, reusable, err := documentCacheKey(context.Background(), input)
	if err != nil || !reusable {
		t.Fatalf("document cache identity reusable=%v err=%v", reusable, err)
	}
	doc, err := parseCachedDocument(context.Background(), input, store)
	if err != nil {
		t.Fatal(err)
	}
	var storedDoc struct {
		ID string `json:"ID"`
	}
	found, err := store.Get(context.Background(), cache.StageStructure, keyBefore, &storedDoc)
	if err != nil || !found || storedDoc.ID != doc.ID {
		t.Fatalf("structure manifest found=%v id=%q err=%v", found, storedDoc.ID, err)
	}
	cachedDoc, err := parseCachedDocument(context.Background(), input, store)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cachedDoc, doc) {
		t.Fatalf("cached structure differs:\nfirst=%#v\ncached=%#v", doc, cachedDoc)
	}

	plan, err := planCachedSegments(context.Background(), doc, path, store)
	if err != nil {
		t.Fatal(err)
	}
	planKey := cache.Key(planManifestVersion, doc.ID, path, "target-70", "maximum-150", "include-code-false")
	var storedPlan struct {
		DocumentID string `json:"DocumentID"`
		Segments   []any  `json:"Segments"`
	}
	found, err = store.Get(context.Background(), cache.StageSegments, planKey, &storedPlan)
	if err != nil || !found || storedPlan.DocumentID != doc.ID || len(storedPlan.Segments) != len(plan.Segments) {
		t.Fatalf("segment manifest found=%v value=%#v err=%v", found, storedPlan, err)
	}
	cachedPlan, err := planCachedSegments(context.Background(), cachedDoc, path, store)
	if err != nil || !reflect.DeepEqual(cachedPlan, plan) {
		t.Fatalf("cached plan differs: plan=%#v cached=%#v err=%v", plan, cachedPlan, err)
	}

	if err := os.WriteFile(path, []byte("Chapter 1\n\nChanged source."), 0o600); err != nil {
		t.Fatal(err)
	}
	keyAfter, _, err := documentCacheKey(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if keyBefore == keyAfter {
		t.Fatal("source edit did not change the structure manifest key")
	}
}

func TestFingerprintFileHonorsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("read me"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fingerprintFile(ctx, path); err == nil {
		t.Fatal("fingerprinted a source with a canceled context")
	}
}

func TestDirectoryStructureIsReparsedAfterFileSetChanges(t *testing.T) {
	root := filepath.Join(t.TempDir(), "docs")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(root, "README.md")
	if err := os.WriteFile(firstPath, []byte("# Intro\n\nFirst document."), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewFileStore(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	input := source.Input{Name: "docs", Path: root}
	if _, reusable, err := documentCacheKey(context.Background(), input); err != nil || reusable {
		t.Fatalf("directory manifest should be recomputed: reusable=%v err=%v", reusable, err)
	}
	first, err := parseCachedDocument(context.Background(), input, store)
	if err != nil {
		t.Fatal(err)
	}
	secondPath := filepath.Join(root, "guide.md")
	if err := os.WriteFile(secondPath, []byte("# Guide\n\nSecond document."), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := parseCachedDocument(context.Background(), input, store)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || len(second.Blocks) <= len(first.Blocks) {
		t.Fatalf("directory structure was reused after its file set changed: first=%#v second=%#v", first, second)
	}
}

func TestRelativeDirectoryPlanCacheIdentityIncludesWorkingDirectory(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()

	firstRoot := filepath.Join(t.TempDir(), "workspace", "docs")
	secondRoot := filepath.Join(t.TempDir(), "workspace", "docs")
	content := []byte("# Shared docs\n\nThis content is the same in both directories.")
	for _, root := range []string{firstRoot, secondRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "README.md"), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := cache.NewFileStore(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := os.Chdir(filepath.Dir(firstRoot)); err != nil {
		t.Fatal(err)
	}
	firstDoc, err := parseCachedDocument(ctx, source.Input{Name: "docs", Path: "docs"}, store)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := planCachedSegments(ctx, firstDoc, "docs", store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Dir(secondRoot)); err != nil {
		t.Fatal(err)
	}
	secondDoc, err := parseCachedDocument(ctx, source.Input{Name: "docs", Path: "docs"}, store)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := planCachedSegments(ctx, secondDoc, "docs", store)
	if err != nil {
		t.Fatal(err)
	}
	if firstDoc.ID != secondDoc.ID {
		t.Fatalf("equivalent directories should have the same content identity: %q != %q", firstDoc.ID, secondDoc.ID)
	}
	if len(firstPlan.Segments) == 0 || len(secondPlan.Segments) == 0 {
		t.Fatal("expected a segment plan for each directory")
	}
	firstSource := firstPlan.Segments[0].Source.Path
	secondSource := secondPlan.Segments[0].Source.Path
	if firstSource != filepath.Join(firstRoot, "README.md") || secondSource != filepath.Join(secondRoot, "README.md") {
		t.Fatalf("relative directory cache returned stale source paths: first=%q second=%q", firstSource, secondSource)
	}
}

func TestAllAudioCachedAvoidsStartingSpeechRuntimeForExport(t *testing.T) {
	store, err := speech.NewCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := segment.Segment{ID: "cached", Seq: 3, SpokenText: "Already synthesized."}
	request := speech.Request{Segment: item, Voice: "af_heart", Speed: 1}
	key, err := speech.CacheKey(kokoro.EngineID, request)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.PrepareOutput(key)
	if err != nil {
		t.Fatal(err)
	}
	writeSmallWAV(t, path)
	if err := store.Store(context.Background(), key, speech.Result{SegmentID: item.ID, Seq: item.Seq, Path: path}); err != nil {
		t.Fatal(err)
	}
	plan := segment.Plan{Segments: []segment.Segment{item}}
	cached, err := allAudioCached(context.Background(), store, plan, "af_heart", 1)
	if err != nil || !cached {
		t.Fatalf("expected complete speech cache, cached=%v err=%v", cached, err)
	}
	report, err := pipeline.Stream(context.Background(), plan, cachedAudioSynthesizer{}, store, nil, pipeline.StreamOptions{Voice: "af_heart", Speed: 1})
	if err != nil {
		t.Fatalf("stream from completed cache: %v", err)
	}
	if report.Generated != 0 || report.Reused != 1 || len(report.Results) != 1 || report.Results[0].Path != path {
		t.Fatalf("cache-only export pipeline generated new audio: %#v", report)
	}
	cached, err = allAudioCached(context.Background(), store, plan, "af_heart", 1.1)
	if err != nil || cached {
		t.Fatalf("different speed should miss, cached=%v err=%v", cached, err)
	}
}

func writeSmallWAV(t *testing.T, path string) {
	t.Helper()
	const frames = uint32(240)
	const sampleRate = uint32(24000)
	dataBytes := frames * 2
	var header [44]byte
	copy(header[:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataBytes)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
	if err := os.WriteFile(path, append(header[:], make([]byte, dataBytes)...), 0o600); err != nil {
		t.Fatal(err)
	}
}
