package speech

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/segment"
)

func TestCacheKeyIncludesSpeechSettingsAndText(t *testing.T) {
	request := Request{Segment: segment.Segment{SpokenText: "Hello."}, Voice: "af_heart", Speed: 1}
	base, err := CacheKey("kokoro", request)
	if err != nil {
		t.Fatal(err)
	}
	request.Voice = "bf_emma"
	voice, _ := CacheKey("kokoro", request)
	request.Voice = "af_heart"
	request.Speed = 1.1
	speed, _ := CacheKey("kokoro", request)
	request.Speed = 1
	request.Segment.SpokenText = "Different text."
	text, _ := CacheKey("kokoro", request)
	model, _ := CacheKey("kokoro-82m-v2", Request{Segment: segment.Segment{SpokenText: "Hello."}, Voice: "af_heart", Speed: 1})
	if base == voice || base == speed || base == text || base == model {
		t.Fatal("cache key ignored a setting that changes the audio")
	}
}

func TestSpeechCacheStoresAndValidatesWAV(t *testing.T) {
	root := t.TempDir()
	store, err := NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	key := "content-key"
	output, err := store.PrepareOutput(key)
	if err != nil {
		t.Fatal(err)
	}
	writeTestWAV(t, output, 24000)
	want := Result{SegmentID: "s1", Seq: 4, Path: output}
	if err := store.Store(context.Background(), key, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Lookup(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if !found || got.SegmentID != want.SegmentID || got.Seq != want.Seq || got.Duration != time.Second {
		t.Fatalf("got found=%v result=%#v", found, got)
	}
}

func TestSpeechCacheTreatsInvalidWAVAsMissAndRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	store, err := NewCache(root)
	if err != nil {
		t.Fatal(err)
	}
	key := "bad-audio"
	path, _ := store.PrepareOutput(key)
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, _ := cache.NewFileStore(root)
	result := Result{SegmentID: "bad", Path: path}
	if err := metadata.PutAtomic(context.Background(), cache.StageSpeech, key, result); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Lookup(context.Background(), key); err != nil || found {
		t.Fatalf("invalid WAV should be a cache miss, found=%v err=%v", found, err)
	}
	if err := store.Store(context.Background(), key, Result{Path: filepath.Join(root, "..", "outside.wav")}); err == nil {
		t.Fatal("accepted output outside cache root")
	}
}

func writeTestWAV(t *testing.T, path string, frames uint32) {
	t.Helper()
	dataBytes := frames * 2
	var header [44]byte
	copy(header[:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataBytes)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], expectedSampleRate)
	binary.LittleEndian.PutUint32(header[28:32], expectedSampleRate*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(make([]byte, dataBytes)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
