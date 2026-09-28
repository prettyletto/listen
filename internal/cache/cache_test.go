package cache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRoundTripsByStageAndKey(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	want := struct {
		Text string `json:"text"`
	}{Text: "translated"}
	if err := store.PutAtomic(context.Background(), StageTranslation, "segment-1", want); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Text string `json:"text"`
	}
	found, err := store.Get(context.Background(), StageTranslation, "segment-1", &got)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got != want {
		t.Fatalf("got found=%v, value=%#v; want %#v", found, got, want)
	}
	found, err = store.Get(context.Background(), StageSpeech, "segment-1", &got)
	if err != nil || found {
		t.Fatalf("different cache stage should miss, found=%v err=%v", found, err)
	}
}

func TestFileStoreRejectsUnsafeIdentifiersAndCanceledWrites(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAtomic(context.Background(), Stage("../../outside"), "key", "value"); err == nil {
		t.Fatal("accepted an unsupported cache stage")
	}
	if err := store.PutAtomic(context.Background(), StageSpeech, "", "value"); err == nil {
		t.Fatal("accepted an empty cache key")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.PutAtomic(ctx, StageSpeech, "cancelled", "value")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled write created cache data: %v", entries)
	}
}

func TestFileStoreDoesNotTurnCorruptCacheIntoMiss(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.artifactPath(StageSpeech, "broken")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	found, err := store.Get(context.Background(), StageSpeech, "broken", &got)
	if err == nil || found {
		t.Fatalf("corrupt artifact should return an error, found=%v err=%v", found, err)
	}
}
