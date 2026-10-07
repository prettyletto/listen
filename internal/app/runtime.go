package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/setup"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
	"github.com/prettyletto/listen/internal/translate"
	"github.com/prettyletto/listen/internal/translate/argos"

	"github.com/prettyletto/listen/internal/translate/openai"
)

type runtimeDeps struct {
	startSynth           func(context.Context, kokoro.Options) (speech.Synthesizer, error)
	newPlayer            func(playback.MPVOptions) playback.Player
	provisionSpeech      func(context.Context, paths.Paths, io.Writer, io.Writer) error
	installTranslator    func(context.Context, paths.Paths) error
	startTranslator      func(context.Context, paths.Paths) (translate.Translator, error)
	startCloudTranslator func(context.Context, settings.Config) (translate.Translator, error)
	discoverCloudModels  func(context.Context, settings.Config) ([]string, error)
	confirmCloudFallback func(io.Reader, io.Writer, string) (bool, error)
	interactiveInput     func(io.Reader) bool
	interactivePlayback  func(io.Reader, io.Writer) bool
	chooseStart          func(context.Context, io.Reader, io.Writer, playerui.Model) (playerui.StartPoint, error)
	startPlaybackScreen  func(io.Reader, io.Writer, playerui.Model) (playerScreen, error)
	startExportScreen    func(io.Reader, io.Writer, playerui.Model) (playerScreen, error)
	exportScreen         playerScreen
	exportResultPath     *string
	translator           translate.Translator
	startupBuffer        time.Duration
	sessionPollInterval  time.Duration
	playbackEnd          *PlaybackEnd
	speechReady          func(context.Context, paths.Paths) (bool, error)
}

func (deps runtimeDeps) withDefaults() runtimeDeps {
	if deps.startSynth == nil {
		deps.startSynth = func(ctx context.Context, options kokoro.Options) (speech.Synthesizer, error) {
			return kokoro.Start(ctx, options)
		}
	}
	if deps.newPlayer == nil {
		deps.newPlayer = func(options playback.MPVOptions) playback.Player {
			return playback.NewMPV(options)
		}
	}
	if deps.provisionSpeech == nil {
		deps.provisionSpeech = func(ctx context.Context, userPaths paths.Paths, stdout, stderr io.Writer) error {
			return (setup.Manager{Stdout: stdout, Stderr: stderr}).Provision(ctx, userPaths)
		}
	}
	if deps.speechReady == nil {
		deps.speechReady = localSpeechRuntimeReady
	}
	if deps.installTranslator == nil {
		deps.installTranslator = func(ctx context.Context, userPaths paths.Paths) error {
			return argos.InstallModel(ctx, userPaths)
		}
	}
	if deps.startTranslator == nil {
		deps.startTranslator = func(ctx context.Context, userPaths paths.Paths) (translate.Translator, error) {
			return argos.Start(ctx, argos.Options{CacheRoot: userPaths.CacheDir})
		}
	}
	if deps.startCloudTranslator == nil {
		deps.startCloudTranslator = func(_ context.Context, config settings.Config) (translate.Translator, error) {
			if config.CloudProvider != "openai" {
				return nil, fmt.Errorf("cloud provider %q is not supported", config.CloudProvider)
			}
			return openai.New(openai.Options{APIKey: os.Getenv("OPENAI_API_KEY"), Model: config.CloudModel})
		}
	}
	if deps.discoverCloudModels == nil {
		deps.discoverCloudModels = func(ctx context.Context, config settings.Config) ([]string, error) {
			if config.CloudProvider != "openai" {
				return nil, fmt.Errorf("cloud provider %q is not supported", config.CloudProvider)
			}
			client, err := openai.New(openai.Options{APIKey: os.Getenv("OPENAI_API_KEY"), Model: config.CloudModel})
			if err != nil {
				return nil, err
			}
			return client.Models(ctx)
		}
	}
	if deps.confirmCloudFallback == nil {
		deps.confirmCloudFallback = confirmCloudFallback
	}
	if deps.interactiveInput == nil {
		deps.interactiveInput = isInteractiveTerminal
	}
	if deps.interactivePlayback == nil {
		deps.interactivePlayback = playerui.Interactive
	}
	if deps.chooseStart == nil {
		deps.chooseStart = playerui.ChooseStart
	}
	if deps.startPlaybackScreen == nil {
		deps.startPlaybackScreen = func(input io.Reader, output io.Writer, model playerui.Model) (playerScreen, error) {
			return playerui.StartPlayback(input, output, model)
		}
	}
	if deps.startExportScreen == nil {
		deps.startExportScreen = func(input io.Reader, output io.Writer, model playerui.Model) (playerScreen, error) {
			return playerui.StartPlayback(input, output, model)
		}
	}
	if deps.sessionPollInterval <= 0 {
		deps.sessionPollInterval = time.Second
	}
	if deps.startupBuffer <= 0 {
		deps.startupBuffer = 20 * time.Second
	}
	return deps
}

func isInteractiveTerminal(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok || file != os.Stdin {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func confirmCloudFallback(stdin io.Reader, stderr io.Writer, prompt string) (bool, error) {
	file, ok := stdin.(*os.File)
	if !ok || file != os.Stdin {
		return false, nil
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false, nil
	}
	if _, err := fmt.Fprintf(stderr, "%s [y/N] ", strings.TrimSpace(prompt)); err != nil {
		return false, err
	}
	answer := make([]byte, 0, 8)
	var one [1]byte
	for len(answer) < 32 {
		if _, err := io.ReadFull(stdin, one[:]); err != nil {
			return false, nil
		}
		if one[0] == '\n' || one[0] == '\r' {
			break
		}
		answer = append(answer, one[0])
	}
	return strings.EqualFold(strings.TrimSpace(string(answer)), "y") || strings.EqualFold(strings.TrimSpace(string(answer)), "yes"), nil
}
