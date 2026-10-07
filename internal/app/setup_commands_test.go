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

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
	"github.com/prettyletto/listen/internal/testkit"
	"github.com/prettyletto/listen/internal/translate"
)

func TestSetupCommandInstallsSelectedVoiceAndSavesAfterHealthCheck(t *testing.T) {
	home := t.TempDir()
	cacheHome := filepath.Join(home, "cache")
	configHome := filepath.Join(home, "config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	var provisions, starts int
	deps := runtimeDeps{
		interactiveInput: func(io.Reader) bool { return true },
		provisionSpeech: func(context.Context, paths.Paths, io.Writer, io.Writer) error {
			provisions++
			return nil
		},
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			starts++
			return appTestSynth{t: t}, nil
		},
	}
	var stdout bytes.Buffer
	if err := runWithRuntime(context.Background(), []string{"setup"}, strings.NewReader("pt-BR\nn\n\n"), &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	config, found, err := settings.Load(userPaths.ConfigDir)
	if err != nil || !found {
		t.Fatalf("setup config found=%v err=%v", found, err)
	}
	if !config.SetupComplete || config.Speech.DefaultVoice != "pf_dora" || config.Voices["pt-BR"] != "pf_dora" {
		t.Fatalf("setup saved config = %#v", config)
	}
	if !strings.Contains(stdout.String(), "Brazilian Portuguese") || !strings.Contains(stdout.String(), "Local speech is ready") {
		t.Fatalf("setup output = %q", stdout.String())
	}
	if provisions != 1 || starts != 1 {
		t.Fatalf("setup provisions=%d health worker starts=%d", provisions, starts)
	}
	if _, err := os.Stat(filepath.Join(userPaths.CacheDir, "setup", "health-check.wav")); !os.IsNotExist(err) {
		t.Fatalf("health sample was not removed: %v", err)
	}

	stdout.Reset()
	if err := runWithRuntime(context.Background(), []string{"setup"}, strings.NewReader("pt-BR\nn\n\n"), &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Listening languages") || provisions != 2 || starts != 2 {
		t.Fatalf("repeat setup output=%q provisions=%d worker starts=%d", stdout.String(), provisions, starts)
	}
}

func TestSetupInstallsAndChecksTheRequestedLocalTranslationPair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	var provisions, speechStarts, installs, translatorStarts int
	deps := runtimeDeps{
		interactiveInput: func(io.Reader) bool { return true },
		provisionSpeech: func(context.Context, paths.Paths, io.Writer, io.Writer) error {
			provisions++
			return nil
		},
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			speechStarts++
			return appTestSynth{t: t}, nil
		},
		installTranslator: func(context.Context, paths.Paths) error {
			installs++
			return nil
		},
		startTranslator: func(context.Context, paths.Paths) (translate.Translator, error) {
			translatorStarts++
			return &testkit.FakeTranslator{}, nil
		},
	}
	var stdout bytes.Buffer
	if err := runWithRuntime(context.Background(), []string{"setup", "--translate", "en:pt-BR"}, strings.NewReader("en-US\n\n\n"), &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	config, found, err := settings.Load(userPaths.ConfigDir)
	if err != nil || !found {
		t.Fatalf("setup config found=%v err=%v", found, err)
	}
	if config.Voices["pt-BR"] != "pf_dora" || !config.SetupComplete {
		t.Fatalf("translation setup config = %#v", config)
	}
	if provisions != 1 || speechStarts != 2 || installs != 1 || translatorStarts != 1 {
		t.Fatalf("provisions=%d speech starts=%d model installs=%d translator checks=%d", provisions, speechStarts, installs, translatorStarts)
	}
	if !strings.Contains(stdout.String(), "local translation for en to pt-BR") || !strings.Contains(stdout.String(), "Source text stays on this device") {
		t.Fatalf("translation setup output = %q", stdout.String())
	}
}

func TestSetupWizardCanConfigureMultipleLanguagesAndLocalTranslation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	var speechStarts, installs, translatorStarts int
	deps := runtimeDeps{
		interactiveInput: func(io.Reader) bool { return true },
		provisionSpeech:  func(context.Context, paths.Paths, io.Writer, io.Writer) error { return nil },
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			speechStarts++
			return appTestSynth{t: t}, nil
		},
		installTranslator: func(context.Context, paths.Paths) error {
			installs++
			return nil
		},
		startTranslator: func(context.Context, paths.Paths) (translate.Translator, error) {
			translatorStarts++
			return &testkit.FakeTranslator{}, nil
		},
	}
	var stdout bytes.Buffer
	input := strings.NewReader("1\nyes\n2\n3\n")
	if err := runWithRuntime(context.Background(), []string{"setup"}, input, &stdout, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	config, found, err := settings.Load(userPaths.ConfigDir)
	if err != nil || !found {
		t.Fatalf("setup config found=%v err=%v", found, err)
	}
	if !config.SetupComplete || config.Speech.DefaultVoice != "af_bella" || config.Voices["en-US"] != "af_bella" || config.Voices["pt-BR"] != "pm_santa" {
		t.Fatalf("wizard saved config = %#v", config)
	}
	if speechStarts != 2 || installs != 1 || translatorStarts != 1 {
		t.Fatalf("speech starts=%d model installs=%d translator checks=%d", speechStarts, installs, translatorStarts)
	}
	for _, expected := range []string{"Which languages do you listen in?", "Install local English-to-Brazilian Portuguese translation?", "Choose a default voice for American English", "Choose a default voice for Brazilian Portuguese"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("wizard output missing %q: %q", expected, stdout.String())
		}
	}
}

func TestSetupTranslationOptionOnlyAcceptsInstalledPair(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{args: nil, want: ""},
		{args: []string{"--translate", "en:pt-br"}, want: "en:pt-BR"},
		{args: []string{"--translate=en:pt-BR"}, want: "en:pt-BR"},
	} {
		got, err := parseSetupTranslation(test.args)
		if err != nil || got != test.want {
			t.Errorf("parseSetupTranslation(%v) = %q, %v; want %q", test.args, got, err, test.want)
		}
	}
	for _, args := range [][]string{
		{"--translate"},
		{"--translate", "en:es"},
		{"--translate", "en:pt-BR", "--translate", "fr:pt-BR"},
		{"--unknown"},
	} {
		if _, err := parseSetupTranslation(args); err == nil {
			t.Errorf("parseSetupTranslation(%v) succeeded", args)
		}
	}
}

func TestSetupFailureDoesNotMarkConfigurationComplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	deps := runtimeDeps{
		provisionSpeech: func(context.Context, paths.Paths, io.Writer, io.Writer) error { return errors.New("offline") },
	}
	err := runWithRuntime(context.Background(), []string{"setup"}, nil, &bytes.Buffer{}, io.Discard, deps)
	if !apperr.HasCode(err, "setup_failed") {
		t.Fatalf("got %v, want setup_failed", err)
	}
	userPaths, _ := paths.FromEnv()
	config, found, loadErr := settings.Load(userPaths.ConfigDir)
	if loadErr != nil || found || config.SetupComplete {
		t.Fatalf("failed setup persisted completed config found=%v config=%#v err=%v", found, config, loadErr)
	}
}

func TestVoiceAndConfigCommandsExposeAndPersistPreferences(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	var stdout bytes.Buffer
	if err := runWithRuntime(context.Background(), []string{"voices"}, nil, &stdout, io.Discard, runtimeDeps{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "American English (en-US)") || !strings.Contains(stdout.String(), "* af_heart") || !strings.Contains(stdout.String(), "Brazilian Portuguese (pt-BR)") {
		t.Fatalf("voice listing = %q", stdout.String())
	}
	for _, command := range [][]string{
		{"config", "set", "voice", "pt-BR", "pf_dora"},
		{"config", "set", "speed", "1.25"},
		{"config", "set", "playback-speed", "1.4"},
		{"config", "set", "ocr-languages", "eng+por"},
		{"config", "set", "cloud-policy", "never"},
		{"config", "set", "cloud-provider", "openai"},
		{"config", "set", "cloud-model", "gpt-test-model"},
	} {
		if err := runWithRuntime(context.Background(), command, nil, &stdout, io.Discard, runtimeDeps{}); err != nil {
			t.Fatalf("command %v: %v", command, err)
		}
	}
	userPaths, _ := paths.FromEnv()
	config, found, err := settings.Load(userPaths.ConfigDir)
	if err != nil || !found {
		t.Fatalf("config found=%v err=%v", found, err)
	}
	if config.Voices["pt-BR"] != "pf_dora" || config.Speech.DefaultSpeed != 1.25 || config.PlaybackRate != 1.4 || config.OCRLanguages != "eng+por" || config.CloudPolicy != "never" || config.CloudProvider != "openai" || config.CloudModel != "gpt-test-model" {
		t.Fatalf("updated preferences = %#v", config)
	}
	stdout.Reset()
	if err := runWithRuntime(context.Background(), []string{"config"}, nil, &stdout, io.Discard, runtimeDeps{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "\"default_speed\": 1.25") || !strings.Contains(stdout.String(), "\"playback_rate\": 1.4") || !strings.Contains(stdout.String(), "\"ocr_languages\": \"eng+por\"") || !strings.Contains(stdout.String(), "\"pt-BR\": \"pf_dora\"") || strings.Contains(stdout.String(), "OPENAI_API_KEY") {
		t.Fatalf("config view = %q", stdout.String())
	}
}

func TestDoctorReportsStorageAndUnavailableManagedRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	var stdout bytes.Buffer
	if err := runWithRuntime(context.Background(), []string{"doctor"}, nil, &stdout, io.Discard, runtimeDeps{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Platform:") || !strings.Contains(stdout.String(), "Kokoro runtime: not ready") || !strings.Contains(stdout.String(), filepath.Join(home, "data", "listen")) {
		t.Fatalf("doctor report = %q", stdout.String())
	}
}

func TestListenUsesConfiguredVoiceAndSpeedUnlessFlagsOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	userPaths, err := paths.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	config := settings.Defaults()
	if err := settings.SetDefaultVoice(&config, "pf_dora"); err != nil {
		t.Fatal(err)
	}
	config.Speech.DefaultSpeed = 1.25
	config.PlaybackRate = 1.4
	if err := settings.Save(context.Background(), userPaths.ConfigDir, config); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(home, "sample.txt")
	if err := os.WriteFile(sourcePath, []byte("A short story."), 0o600); err != nil {
		t.Fatal(err)
	}
	var options []kokoro.Options
	var requests []speech.Request
	var playerOptions []playback.MPVOptions
	deps := runtimeDeps{
		startSynth: func(_ context.Context, option kokoro.Options) (speech.Synthesizer, error) {
			options = append(options, option)
			return appTestSynth{t: t, onRequest: func(request speech.Request) {
				requests = append(requests, request)
			}}, nil
		},
		newPlayer: func(options playback.MPVOptions) playback.Player {
			playerOptions = append(playerOptions, options)
			return nil
		},
	}
	if err := runWithRuntime(context.Background(), []string{sourcePath, "--no-play"}, nil, io.Discard, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	if len(options) != 1 || options[0].Voice != "pf_dora" || len(requests) != 1 || requests[0].Voice != "pf_dora" || requests[0].Speed != 1.25 {
		t.Fatalf("configured synthesis options=%#v requests=%#v", options, requests)
	}
	if err := runWithRuntime(context.Background(), []string{sourcePath, "--no-play", "--voice", "am_adam", "--speed=0.9"}, nil, io.Discard, io.Discard, deps); err != nil {
		t.Fatal(err)
	}
	if len(options) != 2 || options[1].Voice != "am_adam" || len(requests) != 2 || requests[1].Voice != "am_adam" || requests[1].Speed != 0.9 {
		t.Fatalf("explicit synthesis override options=%#v requests=%#v", options, requests)
	}
	defaultPath := filepath.Join(home, "default-rate.txt")
	if err := os.WriteFile(defaultPath, []byte("Default playback speed."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runWithRuntime(context.Background(), []string{defaultPath}, nil, io.Discard, io.Discard, deps); err == nil {
		t.Fatal("expected the fake nil player to stop playback after capturing the configured rate")
	}
	overridePath := filepath.Join(home, "override-rate.txt")
	if err := os.WriteFile(overridePath, []byte("Explicit playback speed."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runWithRuntime(context.Background(), []string{overridePath, "--playback-speed=1.8"}, nil, io.Discard, io.Discard, deps); err == nil {
		t.Fatal("expected the fake nil player to stop playback after capturing the explicit rate")
	}
	if len(playerOptions) != 2 || playerOptions[0].PlaybackSpeed != 1.4 || playerOptions[1].PlaybackSpeed != 1.8 {
		t.Fatalf("configured and explicit player rates = %#v; want 1.4 then 1.8", playerOptions)
	}
}

type appTestSynth struct {
	t         *testing.T
	onRequest func(speech.Request)
}

func (synth appTestSynth) ID() string { return kokoro.EngineID }

func (synth appTestSynth) Synthesize(_ context.Context, request speech.Request) (speech.Result, error) {
	if synth.onRequest != nil {
		synth.onRequest(request)
	}
	writeSmallWAV(synth.t, request.Output)
	duration, err := speech.ValidateWAV(request.Output)
	if err != nil {
		return speech.Result{}, err
	}
	return speech.Result{SegmentID: request.Segment.ID, Seq: request.Segment.Seq, Path: request.Output, Duration: duration}, nil
}

func (appTestSynth) Close() error { return nil }
