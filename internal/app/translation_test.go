package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
	"github.com/prettyletto/listen/internal/testkit"
	"github.com/prettyletto/listen/internal/translate"
)

func TestRunRejectsRemoteTranslatorBeforeReadingSourceOrStartingSpeech(t *testing.T) {
	starts := 0
	translator := &testkit.FakeTranslator{Remote: true}
	deps := runtimeDeps{
		translator: translator,
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			starts++
			return appTestSynth{t: t}, nil
		},
	}
	err := runWithRuntime(context.Background(), []string{"missing.txt", "--translate", "pt-BR"}, strings.NewReader(""), io.Discard, io.Discard, deps)
	if !apperr.HasCode(err, "translation_not_ready") {
		t.Fatalf("remote translator error = %v, want translation_not_ready before source access", err)
	}
	if starts != 0 || translator.CallCount() != 0 {
		t.Fatalf("remote backend crossed privacy guard: synth starts=%d translate calls=%d", starts, translator.CallCount())
	}
}

func TestRunTranslatesBeforeSpeechAndReusesTranslationAndAudioCaches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	sourcePath := filepath.Join(home, "story.md")
	sourceText := "---\nlanguage: en\n---\n\nA sentence that should be translated before speech.\n"
	if err := os.WriteFile(sourcePath, []byte(sourceText), 0o600); err != nil {
		t.Fatal(err)
	}
	translator := &testkit.FakeTranslator{
		TranslateText: func(item segment.Segment, target string) string {
			return target + ": " + item.SourceText
		},
	}
	var synthRequests []speech.Request
	starts := 0
	deps := runtimeDeps{
		translator: translator,
		startSynth: func(_ context.Context, _ kokoro.Options) (speech.Synthesizer, error) {
			starts++
			return appTestSynth{t: t, onRequest: func(request speech.Request) {
				synthRequests = append(synthRequests, request)
			}}, nil
		},
	}
	args := []string{sourcePath, "--no-play", "--translate", "pt-BR"}
	for run := 0; run < 2; run++ {
		if err := runWithRuntime(context.Background(), args, strings.NewReader(""), io.Discard, io.Discard, deps); err != nil {
			t.Fatalf("run %d: %v", run+1, err)
		}
	}
	if starts != 1 || translator.CallCount() != 1 || len(synthRequests) != 1 {
		t.Fatalf("repeat run regenerated cached work: starts=%d translations=%d speech requests=%#v", starts, translator.CallCount(), synthRequests)
	}
	request := synthRequests[0]
	if request.Segment.SourceLang != "en" || request.Segment.TargetLang != "pt-BR" || request.Segment.SpokenText != "pt-BR: "+request.Segment.SourceText {
		t.Fatalf("speech did not receive the committed translation: %#v", request)
	}
	if request.Voice != "pf_dora" {
		t.Fatalf("translated speech used voice %q, want Brazilian Portuguese voice pf_dora", request.Voice)
	}
	translationRequests := translator.RequestSnapshot()
	if len(translationRequests) != 1 || translationRequests[0].TargetLanguage != "pt-BR" || len(translationRequests[0].Segments) != 1 {
		t.Fatalf("translator request = %#v", translationRequests)
	}
}

func TestRunWarnsWhenLocalTranslationCannotKeepUpWithPlayback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	sourcePath := filepath.Join(home, "slow.md")
	if err := os.WriteFile(sourcePath, []byte("---\nlanguage: en\n---\n\nTiny."), 0o600); err != nil {
		t.Fatal(err)
	}
	translator := &testkit.FakeTranslator{
		Delay:         1100 * time.Millisecond,
		TranslateText: func(segment.Segment, string) string { return "Oi." },
	}
	var stderr bytes.Buffer
	deps := runtimeDeps{
		translator:    translator,
		startupBuffer: time.Nanosecond,
		newPlayer: func(playback.MPVOptions) playback.Player {
			return &resumeTestPlayer{}
		},
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			return appTestSynth{t: t}, nil
		},
	}
	if err := runWithRuntime(context.Background(), []string{sourcePath, "--translate", "pt-BR"}, nil, io.Discard, &stderr, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Local translation is slower than playback") || translator.CallCount() != 1 {
		t.Fatalf("slow playback warning=%q translator calls=%d", stderr.String(), translator.CallCount())
	}
}

func TestRunRejectsUnsupportedLocalLanguagePairBeforeTranslationOrSpeech(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	sourcePath := filepath.Join(home, "french.md")
	if err := os.WriteFile(sourcePath, []byte("---\nlanguage: fr\n---\n\nBonjour."), 0o600); err != nil {
		t.Fatal(err)
	}
	translator := &testkit.FakeTranslator{LanguagePairs: map[string]bool{"en:pt-BR": true}}
	starts := 0
	deps := runtimeDeps{
		translator: translator,
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			starts++
			return appTestSynth{t: t}, nil
		},
	}
	err := runWithRuntime(context.Background(), []string{sourcePath, "--translate", "pt-BR", "--no-play"}, nil, io.Discard, io.Discard, deps)
	if !apperr.HasCode(err, "translation_pair_unsupported") || translator.CallCount() != 0 || starts != 0 {
		t.Fatalf("unsupported pair error=%v translations=%d speech starts=%d", err, translator.CallCount(), starts)
	}
}

func TestCloudFallbackRequiresPolicyAndPinsProviderAcrossMajorUnits(t *testing.T) {
	tests := []struct {
		name       string
		policy     string
		confirm    bool
		wantPrompt int
		wantCloud  bool
		noLocal    bool
	}{
		{name: "never never contacts cloud", policy: "never"},
		{name: "ask decline keeps source local", policy: "ask", confirm: false, wantPrompt: 1},
		{name: "ask approval permits cloud", policy: "ask", confirm: true, wantPrompt: 1, wantCloud: true},
		{name: "allowed switches without prompt", policy: "allowed", wantCloud: true},
		{name: "allowed uses cloud if local setup is unavailable", policy: "allowed", wantCloud: true, noLocal: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			userPaths, err := paths.FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			config := settings.Defaults()
			config.CloudPolicy = test.policy
			if err := settings.Save(context.Background(), userPaths.ConfigDir, config); err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(home, "book.md")
			sourceText := "---\nlanguage: en\n---\n\n# Example Book\n\nPreface text.\n\n## Part I\n\n### Chapter 1 — Beginning\n\nTiny sentence.\n\n### Chapter 2 — Continuation\n\nAnother tiny sentence.\n"
			if err := os.WriteFile(sourcePath, []byte(sourceText), 0o600); err != nil {
				t.Fatal(err)
			}
			local := &testkit.FakeTranslator{Delay: 1100 * time.Millisecond, TranslateText: func(segment.Segment, string) string { return "Local." }}
			cloud := &testkit.FakeTranslator{BackendID: "fake-cloud:gpt-test", Remote: true, TranslateText: func(segment.Segment, string) string { return "Cloud." }}
			factoryCalls, prompts := 0, 0
			var stderr bytes.Buffer
			deps := runtimeDeps{
				translator: local,
				startCloudTranslator: func(context.Context, settings.Config) (translate.Translator, error) {
					factoryCalls++
					return cloud, nil
				},
				confirmCloudFallback: func(_ io.Reader, output io.Writer, prompt string) (bool, error) {
					prompts++
					if !strings.Contains(prompt, "receive this book's text") {
						t.Fatalf("cloud prompt did not explain the upload: %q", prompt)
					}
					_, _ = io.WriteString(output, prompt+" [y/N] ")
					return test.confirm, nil
				},
				startupBuffer: time.Nanosecond,
				newPlayer:     func(playback.MPVOptions) playback.Player { return &resumeTestPlayer{} },
				startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
					return appTestSynth{t: t}, nil
				},
			}
			args := []string{sourcePath, "--translate", "pt-BR"}
			if test.noLocal {
				deps.translator = nil
				deps.startTranslator = func(context.Context, paths.Paths) (translate.Translator, error) {
					return nil, errors.New("local model is missing")
				}
				args = append(args, "--no-play")
			}
			if err := runWithRuntime(context.Background(), args, strings.NewReader(""), io.Discard, &stderr, deps); err != nil {
				t.Fatalf("listen source: %v", err)
			}
			if prompts != test.wantPrompt {
				t.Fatalf("cloud confirmation count = %d, want %d", prompts, test.wantPrompt)
			}
			if factoryCalls != btoi(test.wantCloud) || (cloud.CallCount() > 0) != test.wantCloud {
				t.Fatalf("cloud factory=%d calls=%d wantCloud=%v", factoryCalls, cloud.CallCount(), test.wantCloud)
			}
			if !test.wantCloud && local.CallCount() == 0 {
				t.Fatal("local translator did not handle the source after cloud fallback was denied")
			}
			if test.policy == "never" && (factoryCalls != 0 || cloud.CallCount() != 0) {
				t.Fatalf("never policy contacted cloud: factory=%d translations=%d", factoryCalls, cloud.CallCount())
			}
			if test.policy == "allowed" && prompts != 0 {
				t.Fatalf("allowed policy prompted %d times", prompts)
			}
			if test.wantCloud {
				requests := cloud.RequestSnapshot()
				units := make(map[string]bool)
				for _, request := range requests {
					for _, item := range request.Segments {
						units[item.MajorUnitID] = true
					}
				}
				if len(units) < 2 {
					t.Fatalf("cloud provider was not held across both major units: %#v", requests)
				}
			}
		})
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
