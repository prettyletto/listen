package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

func TestRecommendedSetupCanCancelAndRetryBeforeSavingCompletion(t *testing.T) {
	userPaths := paths.Paths{
		ConfigDir: filepath.Join(t.TempDir(), "config"),
		DataDir:   filepath.Join(t.TempDir(), "data"),
		CacheDir:  filepath.Join(t.TempDir(), "cache"),
	}
	config := settings.Defaults()
	started := make(chan struct{})
	attempts := 0
	deps := runtimeDeps{
		provisionSpeech: func(ctx context.Context, _ paths.Paths, _, _ io.Writer) error {
			attempts++
			if attempts == 1 {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			return appTestSynth{t: t}, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- prepareLocalSpeechRuntime(ctx, userPaths, config, io.Discard, io.Discard, deps, nil)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("setup did not reach its cancellable provisioner")
	}
	cancel()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled setup error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setup did not stop after cancellation")
	}

	progress := ""
	if err := prepareLocalSpeechRuntime(context.Background(), userPaths, config, io.Discard, io.Discard, deps, func(note string) { progress = note }); err != nil {
		t.Fatalf("retry setup: %v", err)
	}
	config.SetupComplete = true
	if err := settings.Save(context.Background(), userPaths.ConfigDir, config); err != nil {
		t.Fatalf("save successful retry: %v", err)
	}
	got, found, err := settings.Load(userPaths.ConfigDir)
	if err != nil || !found || !got.SetupComplete || attempts != 2 || progress != "Checking your local voices…" {
		t.Fatalf("successful retry config=%#v found=%v attempts=%d progress=%q err=%v", got, found, attempts, progress, err)
	}
}

func TestPreferencesPersistVoiceMappingsAndBothSpeedsWithoutChangingTranslationConsent(t *testing.T) {
	config := settings.Defaults()
	config.CloudPolicy = "ask"
	preferences := preferencesForConfig(config)
	preferences.DefaultVoice = "pf_dora"
	preferences.Voices["en-GB"] = "bf_emma"
	preferences.SpeechSpeed = 1.3
	preferences.PlaybackRate = 1.6
	if err := applyPreferences(&config, preferences); err != nil {
		t.Fatalf("apply preferences: %v", err)
	}
	directory := filepath.Join(t.TempDir(), "config")
	if err := settings.Save(context.Background(), directory, config); err != nil {
		t.Fatalf("save preferences: %v", err)
	}
	got, found, err := settings.Load(directory)
	if err != nil || !found {
		t.Fatalf("load saved preferences found=%v err=%v", found, err)
	}
	if got.Speech.DefaultVoice != "pf_dora" || got.Voices["en-GB"] != "bf_emma" || got.Speech.DefaultSpeed != 1.3 || got.PlaybackRate != 1.6 {
		t.Fatalf("saved voice and speed preferences = %#v", got)
	}
	if got.CloudPolicy != "ask" {
		t.Fatalf("choosing a voice changed cloud translation consent to %q", got.CloudPolicy)
	}
}

func TestAdvancedPreferencesPersistOCRLanguagesAndTranslationConsent(t *testing.T) {
	config := settings.Defaults()
	preferences := preferencesForConfig(config)
	preferences.OCRLanguages = "eng+por"
	preferences.CloudPolicy = "never"
	if err := applyPreferences(&config, preferences); err != nil {
		t.Fatalf("apply advanced preferences: %v", err)
	}
	directory := filepath.Join(t.TempDir(), "config")
	if err := settings.Save(context.Background(), directory, config); err != nil {
		t.Fatalf("save advanced preferences: %v", err)
	}
	got, found, err := settings.Load(directory)
	if err != nil || !found {
		t.Fatalf("load advanced preferences found=%v err=%v", found, err)
	}
	if got.OCRLanguages != "eng+por" || got.CloudPolicy != "never" {
		t.Fatalf("advanced settings were not persisted: OCR=%q cloud=%q", got.OCRLanguages, got.CloudPolicy)
	}
}

type previewLifecycleSynth struct {
	t      *testing.T
	closed chan struct{}
}

func (s previewLifecycleSynth) ID() string { return kokoro.EngineID }

func (s previewLifecycleSynth) Synthesize(_ context.Context, request speech.Request) (speech.Result, error) {
	writeSmallWAV(s.t, request.Output)
	duration, err := speech.ValidateWAV(request.Output)
	if err != nil {
		return speech.Result{}, err
	}
	return speech.Result{SegmentID: request.Segment.ID, Seq: request.Segment.Seq, Path: request.Output, Duration: duration}, nil
}

func (s previewLifecycleSynth) Close() error {
	close(s.closed)
	return nil
}

type previewLifecyclePlayer struct {
	started chan struct{}
	item    chan playback.Item
	closed  chan struct{}
	close   sync.Once
}

func (p *previewLifecyclePlayer) Start(context.Context) error {
	close(p.started)
	return nil
}

func (p *previewLifecyclePlayer) Append(ctx context.Context, item playback.Item) error {
	select {
	case p.item <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *previewLifecyclePlayer) Position(context.Context) (uint64, time.Duration, error) {
	return 0, 0, nil
}

func (p *previewLifecyclePlayer) Wait(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func (p *previewLifecyclePlayer) Close() error {
	p.close.Do(func() { close(p.closed) })
	return nil
}

func TestVoicePreviewCancellationClosesWorkerPlayerAndRemovesSample(t *testing.T) {
	cacheRoot := t.TempDir()
	userPaths := paths.Paths{CacheDir: cacheRoot}
	synthClosed := make(chan struct{})
	player := &previewLifecyclePlayer{started: make(chan struct{}), item: make(chan playback.Item, 1), closed: make(chan struct{})}
	deps := runtimeDeps{
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			return previewLifecycleSynth{t: t, closed: synthClosed}, nil
		},
		newPlayer: func(playback.MPVOptions) playback.Player { return player },
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- playVoicePreview(ctx, userPaths, "af_heart", 1, 1, deps) }()
	var samplePath string
	select {
	case item := <-player.item:
		samplePath = item.Path
	case <-time.After(3 * time.Second):
		t.Fatal("preview audio was not queued")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled preview error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("preview did not stop after cancellation")
	}
	for name, signal := range map[string]<-chan struct{}{"speech worker": synthClosed, "audio player": player.closed} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Errorf("preview cancellation did not close %s", name)
		}
	}
	if samplePath == "" {
		t.Fatal("preview sample path was not recorded")
	}
	if _, err := os.Stat(filepath.Dir(samplePath)); !os.IsNotExist(err) {
		t.Fatalf("temporary preview directory remains after cancellation: %v", err)
	}
}
