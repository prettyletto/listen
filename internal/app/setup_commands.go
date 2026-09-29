package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/setup"
	"github.com/prettyletto/listen/internal/speech/kokoro"
	"github.com/prettyletto/listen/internal/translate"
	"github.com/prettyletto/listen/internal/translate/argos"
)

func setupCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, deps runtimeDeps) error {
	translationPair, err := parseSetupTranslation(args)
	if err != nil {
		return apperr.New("invalid_arguments", err.Error(), "Run listen setup or listen setup --translate en:pt-BR.", err)
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		return apperr.New("storage_unavailable", "Could not prepare local speech storage.", "Check your home directory and XDG paths.", err)
	}
	config, _, err := settings.Load(userPaths.ConfigDir)
	if err != nil {
		return apperr.New("config_unavailable", "Could not read listen preferences.", "Run listen config to inspect or repair the local configuration.", err)
	}
	if deps.interactiveInput(stdin) {
		config, translationPair, err = runSetupWizard(ctx, stdin, stdout, config, translationPair)
		if err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return apperr.New("invalid_setup_choice", "Could not use those setup choices.", err.Error(), err)
		}
	} else {
		if translationPair != "" {
			if err := ensureTranslationVoice(&config, translationPair); err != nil {
				return apperr.New("invalid_voice", "The translated language voice is unavailable.", "Run listen voices to see available local voices.", err)
			}
		}
		if _, ok := kokoro.FindVoice(config.Speech.DefaultVoice); !ok {
			return apperr.New("invalid_voice", "The configured local voice is unavailable.", "Run listen config set voice af_heart to choose a supported voice.", nil)
		}
		if len(config.Voices) == 0 {
			if err := settings.SetDefaultVoice(&config, config.Speech.DefaultVoice); err != nil {
				return apperr.New("invalid_voice", "The configured local voice is unavailable.", "Run listen voices to choose a supported voice.", err)
			}
		}
	}
	voice, ok := kokoro.FindVoice(config.Speech.DefaultVoice)
	if !ok {
		return apperr.New("invalid_voice", "The configured local voice is unavailable.", "Run listen config set voice af_heart to choose a supported voice.", nil)
	}
	message := fmt.Sprintf("Preparing local speech with %s (%s). This can take several minutes the first time.\n", voice.LocaleName, voice.ID)
	if translationPair != "" {
		message = fmt.Sprintf("Preparing local speech for %s (%s) and local translation for %s. This can take several minutes the first time.\n", voice.LocaleName, voice.ID, strings.ReplaceAll(translationPair, ":", " to "))
	}
	if _, err := fmt.Fprint(stdout, message); err != nil {
		return err
	}
	if _, err := exec.LookPath("mpv"); err != nil {
		if _, writeErr := fmt.Fprintln(stdout, "Warning: mpv is not installed; install it with your Linux package manager before playback."); writeErr != nil {
			return writeErr
		}
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if _, writeErr := fmt.Fprintln(stdout, "Warning: FFmpeg is not installed; install it with your Linux package manager before audiobook export."); writeErr != nil {
			return writeErr
		}
	}
	if err := deps.provisionSpeech(ctx, userPaths, stdout, stderr); err != nil {
		if ctx.Err() != nil {
			return interrupted(ctx.Err())
		}
		return apperr.New("setup_failed", "Could not install the local speech runtime.", "Check your internet connection and install uv, then run listen setup again.", err)
	}
	voiceIDs := []string{config.Speech.DefaultVoice}
	for _, configuredVoice := range config.Voices {
		if configuredVoice != config.Speech.DefaultVoice {
			voiceIDs = append(voiceIDs, configuredVoice)
		}
	}
	if len(voiceIDs) > 1 {
		sort.Strings(voiceIDs[1:])
	}
	for _, voiceID := range voiceIDs {
		if err := setup.HealthSynthesize(ctx, userPaths.CacheDir, voiceID, deps.startSynth); err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return apperr.New("setup_failed", "The local speech runtime did not pass its audio check.", "Run listen doctor for details, then run listen setup again.", err)
		}
	}
	if translationPair != "" {
		if _, err := fmt.Fprintf(stdout, "Installing the local translator for %s. Source text stays on this device.\n", strings.ReplaceAll(translationPair, ":", " to ")); err != nil {
			return err
		}
		if err := deps.installTranslator(ctx, userPaths); err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return apperr.New("setup_failed", "Could not install local translation.", "Check your internet connection and storage, then run listen setup --translate en:pt-BR again.", err)
		}
		translator, err := deps.startTranslator(ctx, userPaths)
		if err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return apperr.New("setup_failed", "The local translation model did not pass its startup check.", "Run listen doctor for details, then run listen setup --translate en:pt-BR again.", err)
		}
		if translator == nil {
			return apperr.New("setup_failed", "The local translation model did not pass its startup check.", "Run listen doctor for details, then run listen setup --translate en:pt-BR again.", nil)
		}
		capabilities, capabilityErr := translator.Capabilities(ctx)
		if closer, ok := translator.(interface{ Close() error }); ok {
			defer closer.Close()
		}
		if capabilityErr != nil || !capabilities.Local || !capabilities.SupportsLanguagePair("en", "pt-BR") {
			if capabilityErr == nil {
				capabilityErr = errors.New("installed translation model does not support the requested language pair")
			}
			return apperr.New("setup_failed", "The local translation model did not pass its language check.", "Run listen doctor for details, then run listen setup --translate en:pt-BR again.", capabilityErr)
		}
	}
	config.SetupComplete = true
	if err := settings.Save(ctx, userPaths.ConfigDir, config); err != nil {
		return apperr.New("config_save_failed", "Speech is ready, but listen could not save its preferences.", "Check that your configuration directory is writable, then run listen setup again.", err)
	}
	_, err = fmt.Fprintln(stdout, "✓ Local speech is ready. Run listen <source> to start listening.")
	return err
}

func parseSetupTranslation(args []string) (string, error) {
	pair := ""
	for index := 0; index < len(args); index++ {
		name, value, hasValue := strings.Cut(args[index], "=")
		if name != "--translate" {
			return "", fmt.Errorf("listen setup does not recognize %q", args[index])
		}
		if pair != "" {
			return "", errors.New("listen setup accepts one translation pair")
		}
		if !hasValue {
			if index+1 >= len(args) {
				return "", errors.New("--translate needs a source:target pair")
			}
			index++
			value = args[index]
		}
		source, target, ok := strings.Cut(value, ":")
		if !ok || source == "" || target == "" {
			return "", errors.New("translation pair must use source:target form")
		}
		target, err := translate.NormalizeLanguageTag(target)
		if err != nil {
			return "", err
		}
		canonical, err := translate.LanguagePair(source, target)
		if err != nil || canonical != argos.LanguagePair {
			return "", fmt.Errorf("local translation setup currently supports %s", argos.LanguagePair)
		}
		pair = canonical
	}
	return pair, nil
}

func voicesCommand(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return apperr.New("invalid_arguments", "listen voices does not take options.", "Run listen voices.", nil)
	}
	if err := ctx.Err(); err != nil {
		return interrupted(err)
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		return apperr.New("storage_unavailable", "Could not read listen preferences.", "Check your home directory and XDG paths.", err)
	}
	config, _, err := settings.Load(userPaths.ConfigDir)
	if err != nil {
		return apperr.New("config_unavailable", "Could not read listen preferences.", "Run listen config to inspect or repair the local configuration.", err)
	}
	voices := kokoro.Voices()
	lastLocale := ""
	for _, voice := range voices {
		if voice.Locale != lastLocale {
			if _, err := fmt.Fprintf(stdout, "%s (%s)\n", voice.LocaleName, voice.Locale); err != nil {
				return err
			}
			lastLocale = voice.Locale
		}
		marker := "  "
		label := voice.Name
		if voice.ID == config.Speech.DefaultVoice {
			marker = "* "
			label += " (default)"
		} else if config.Voices[voice.Locale] == voice.ID {
			marker = "+ "
			label += " (configured)"
		}
		if _, err := fmt.Fprintf(stdout, "%s%-12s %s\n", marker, voice.ID, label); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(stdout, "Voice files are downloaded locally when you run listen setup for their language.")
	return err
}

func configCommand(ctx context.Context, args []string, stdout io.Writer) error {
	userPaths, err := paths.FromEnv()
	if err != nil {
		return apperr.New("storage_unavailable", "Could not locate listen preferences.", "Check your home directory and XDG paths.", err)
	}
	config, _, err := settings.Load(userPaths.ConfigDir)
	if err != nil {
		return apperr.New("config_unavailable", "Could not read listen preferences.", "Run listen config to inspect or repair the local configuration.", err)
	}
	if len(args) == 0 {
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stdout, "%s\nConfig file: %s\n", data, settings.Path(userPaths.ConfigDir)); err != nil {
			return err
		}
		return nil
	}
	if len(args) < 3 || args[0] != "set" {
		return apperr.New("invalid_arguments", "The config command did not recognize that change.", "Use listen config set voice <id>, listen config set speed <0.5–2.0>, listen config set cloud-policy <ask|never|allowed>, or set cloud-model <model-id>.", nil)
	}
	switch args[1] {
	case "voice":
		switch len(args) {
		case 3:
			err = settings.SetDefaultVoice(&config, args[2])
		case 4:
			err = settings.SetLocaleVoice(&config, args[2], args[3])
		default:
			err = errors.New("voice setting accepts a voice ID, or a locale and voice ID")
		}
	case "speed":
		if len(args) != 3 {
			err = errors.New("speed setting needs one value")
		} else {
			var speed float64
			speed, err = strconv.ParseFloat(args[2], 64)
			if err == nil && (speed < 0.5 || speed > 2 || speed != speed) {
				err = errors.New("default speech speed must be between 0.5 and 2.0")
			}
			if err == nil {
				config.Speech.DefaultSpeed = speed
			}
		}
	case "cloud-policy":
		if len(args) != 3 {
			err = errors.New("cloud policy setting needs one value")
		} else if args[2] != "ask" && args[2] != "never" && args[2] != "allowed" {
			err = errors.New("cloud policy must be ask, never, or allowed")
		} else {
			config.CloudPolicy = args[2]
		}
	case "cloud-provider":
		if len(args) != 3 {
			err = errors.New("cloud provider setting needs one value")
		} else if args[2] != "openai" {
			err = errors.New("cloud provider must be openai")
		} else {
			config.CloudProvider = args[2]
		}
	case "cloud-model":
		if len(args) != 3 {
			err = errors.New("cloud model setting needs one value")
		} else {
			config.CloudModel = args[2]
			err = settings.Validate(config)
		}
	default:
		err = fmt.Errorf("unknown preference %q", args[1])
	}
	if err != nil {
		return apperr.New("invalid_config", err.Error(), "Run listen voices or listen config to see supported preferences.", err)
	}
	if err := settings.Save(ctx, userPaths.ConfigDir, config); err != nil {
		return apperr.New("config_save_failed", "Could not save listen preferences.", "Check that your configuration directory is writable.", err)
	}
	_, err = fmt.Fprintln(stdout, "✓ Saved listen preferences.")
	return err
}

func doctorCommand(ctx context.Context, args []string, stdout io.Writer, deps runtimeDeps) error {
	if len(args) != 0 {
		return apperr.New("invalid_arguments", "listen doctor does not take options.", "Run listen doctor.", nil)
	}
	if err := ctx.Err(); err != nil {
		return interrupted(err)
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		return apperr.New("storage_unavailable", "Could not inspect listen storage.", "Check your home directory and XDG paths.", err)
	}
	config, configFound, configErr := settings.Load(userPaths.ConfigDir)
	if _, err := fmt.Fprintf(stdout, "listen doctor\nPlatform: %s/%s\nData: %s\nCache: %s\nConfig: %s\n", runtime.GOOS, runtime.GOARCH, userPaths.DataDir, userPaths.CacheDir, settings.Path(userPaths.ConfigDir)); err != nil {
		return err
	}
	if configErr != nil {
		if _, err := fmt.Fprintf(stdout, "Configuration: invalid (%v)\n", configErr); err != nil {
			return err
		}
	} else if configFound {
		setupState := "preferences saved; setup incomplete"
		if config.SetupComplete {
			setupState = "setup complete"
		}
		if _, err := fmt.Fprintf(stdout, "Configuration: %s (default voice %s; cloud policy %s; cloud translator %s/%s)\n", setupState, config.Speech.DefaultVoice, config.CloudPolicy, config.CloudProvider, config.CloudModel); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(stdout, "Configuration: defaults (run listen setup to save preferences)"); err != nil {
		return err
	}
	for _, item := range []struct {
		program string
		args    []string
	}{
		{program: "uv", args: []string{"--version"}},
		{program: "mpv", args: []string{"--version"}},
		{program: "ffmpeg", args: []string{"-version"}},
	} {
		if err := writeProgramStatus(ctx, stdout, item.program, item.args); err != nil {
			return err
		}
	}
	manager := setup.Manager{}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	version, runtimeErr := manager.Verify(checkCtx, userPaths)
	cancel()
	if runtimeErr != nil {
		if _, err := fmt.Fprintf(stdout, "Kokoro runtime: not ready (%v)\n", runtimeErr); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(stdout, "Kokoro runtime: ready (%s, %s)\n", version, setup.PythonPath(userPaths)); err != nil {
		return err
	}
	if !argos.ModelInstalled(argos.ModelRoot(userPaths)) {
		if _, err := fmt.Fprintf(stdout, "Local translation: not installed (run listen setup --translate %s)\n", argos.LanguagePair); err != nil {
			return err
		}
	} else {
		translationCtx, translationCancel := context.WithTimeout(ctx, 15*time.Second)
		translator, translationErr := deps.startTranslator(translationCtx, userPaths)
		if translationErr == nil && translator == nil {
			translationErr = errors.New("local translation worker did not start")
		}
		if translationErr == nil {
			var capabilities translate.Capabilities
			capabilities, translationErr = translator.Capabilities(translationCtx)
			if closer, ok := translator.(interface{ Close() error }); ok {
				if closeErr := closer.Close(); translationErr == nil {
					translationErr = closeErr
				}
			}
			if translationErr == nil && (!capabilities.Local || !capabilities.SupportsLanguagePair("en", "pt-BR")) {
				translationErr = errors.New("installed translator does not support English to Brazilian Portuguese")
			}
		}
		translationCancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if translationErr != nil {
			if _, err := fmt.Fprintf(stdout, "Local translation: not ready (%v)\n", translationErr); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(stdout, "Local translation: ready (English to Brazilian Portuguese)\n"); err != nil {
			return err
		}
	}
	voice := "af_heart"
	if configErr == nil {
		voice = config.Speech.DefaultVoice
	}
	modelCache := filepath.Join(userPaths.CacheDir, "huggingface", "hub")
	modelState := "not downloaded yet; run listen setup"
	if entries, readErr := os.ReadDir(modelCache); readErr == nil && len(entries) > 0 {
		modelState = "present"
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		modelState = "unreadable"
	}
	_, err = fmt.Fprintf(stdout, "Voice selected: %s\nModel cache: %s (%s)\n", voice, modelCache, modelState)
	return err
}

func modelsCommand(ctx context.Context, args []string, stdout io.Writer, deps runtimeDeps) error {
	if len(args) != 0 {
		return apperr.New("invalid_arguments", "listen models does not take options.", "Run listen models to discover OpenAI translation models.", nil)
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		return apperr.New("storage_unavailable", "Could not inspect listen configuration.", "Check your home directory and XDG paths.", err)
	}
	config, _, err := settings.Load(userPaths.ConfigDir)
	if err != nil {
		return apperr.New("config_unavailable", "Could not read listen preferences.", "Run listen config to inspect or repair the local configuration.", err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	models, err := deps.discoverCloudModels(checkCtx, config)
	if err != nil {
		return apperr.New("models_unavailable", "Could not list cloud translation models.", "Set OPENAI_API_KEY in your environment, check network access, and run listen models again.", err)
	}
	if _, err := fmt.Fprintln(stdout, "OpenAI models (model discovery sends no source text):"); err != nil {
		return err
	}
	for _, model := range models {
		if _, err := fmt.Fprintln(stdout, model); err != nil {
			return err
		}
	}
	return nil
}

func writeProgramStatus(ctx context.Context, stdout io.Writer, program string, args []string) error {
	path, err := exec.LookPath(program)
	if err != nil {
		_, writeErr := fmt.Fprintf(stdout, "%s: missing\n", program)
		return writeErr
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	command := exec.CommandContext(checkCtx, path, args...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, writeErr := fmt.Fprintf(stdout, "%s: found at %s, version check failed\n", program, path)
		return writeErr
	}
	version := strings.TrimSpace(strings.SplitN(output.String(), "\n", 2)[0])
	_, err = fmt.Fprintf(stdout, "%s: %s (%s)\n", program, path, version)
	return err
}
