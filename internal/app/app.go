package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/domain"
	audiobook "github.com/prettyletto/listen/internal/export"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/pipeline"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/source"
	directorysource "github.com/prettyletto/listen/internal/source/directory"
	epubsource "github.com/prettyletto/listen/internal/source/epub"
	htmlsource "github.com/prettyletto/listen/internal/source/html"
	markdownsource "github.com/prettyletto/listen/internal/source/markdown"
	pdfsource "github.com/prettyletto/listen/internal/source/pdf"
	textsource "github.com/prettyletto/listen/internal/source/text"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
	"github.com/prettyletto/listen/internal/translate"
	"github.com/prettyletto/listen/internal/ui"
	"github.com/prettyletto/listen/internal/viability"
)

const usage = `listen — make readable things listenable

Usage:
  listen <source> [--translate BCP-47] [--voice ID] [--speed RATE] [--playback-speed RATE]
  listen <source> --export [DIRECTORY]
  listen <source> --no-play
  listen - --no-play
  listen help

Options:
  --translate BCP-47     Translate to this language using local/cloud policy
  --voice ID             Local speech voice (default af_heart)
  --speed RATE           Speech generation rate from 0.5 to 2.0 (default 1)
  --playback-speed RATE  Playback rate from 0.5 to 3.0 (default 1)
  --no-play              Generate and cache audio without opening a player
  --export [DIRECTORY]   Create a reusable MP3/M4B audiobook bundle
  -h, --help             Show this help

Commands:
  setup                  Install local speech; add --translate en:pt-BR for translation
  voices                 List available local voices
  config                 Show or update reader preferences
  models                 List available OpenAI translation models
  doctor                 Inspect local runtime and player readiness

Current sources: UTF-8 text files, Markdown files, HTML documents, EPUB books, text-based PDFs, and documentation directories.
`

type listenOptions struct {
	source         string
	targetLanguage string
	voice          string
	speed          float64
	playbackSpeed  float64
	noPlay         bool
	exportAudio    bool
	exportPath     string
	help           bool
	version        bool
	voiceExplicit  bool
	speedExplicit  bool
}

// Run dispatches the reader command. Format-specific complexity and process
// orchestration stay behind this seam rather than leaking into main.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return runWithRuntime(ctx, args, stdin, stdout, stderr, runtimeDeps{})
}

func runWithRuntime(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, deps runtimeDeps) error {
	deps = deps.withDefaults()
	if ctx == nil {
		return apperr.New("invalid_context", "The command could not start.", "Run listen again from a normal shell.", nil)
	}
	if err := ctx.Err(); err != nil {
		return interrupted(err)
	}
	if len(args) == 0 {
		return writeUsage(stdout)
	}
	switch args[0] {
	case "help", "--help", "-h":
		return writeUsage(stdout)
	case "--version", "version":
		_, err := fmt.Fprintln(stdout, "listen development")
		return err
	case "setup":
		return setupCommand(ctx, args[1:], stdin, stdout, stderr, deps)
	case "voices":
		return voicesCommand(ctx, args[1:], stdout)
	case "config":
		return configCommand(ctx, args[1:], stdout)
	case "doctor":
		return doctorCommand(ctx, args[1:], stdout, deps)
	case "models":
		return modelsCommand(ctx, args[1:], stdout, deps)
	case "inspect":
		return apperr.New("command_not_ready", "That command is not available yet.", "Run listen <source.txt> to listen to a text file.", nil)
	}
	options, err := parseListenOptions(args)
	if err != nil {
		return apperr.New("invalid_arguments", err.Error(), "Run listen --help to see supported options.", err)
	}
	if options.help {
		return writeUsage(stdout)
	}
	if options.version {
		_, err := fmt.Fprintln(stdout, "listen development")
		return err
	}
	return listenSource(ctx, options, stdin, stderr, deps)
}

func parseListenOptions(args []string) (listenOptions, error) {
	options := listenOptions{voice: "af_heart", speed: 1, playbackSpeed: 1}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := strings.Cut(arg, "=")
		if arg == "-" {
			positional = append(positional, arg)
			continue
		}
		switch name {
		case "--help", "-h":
			options.help = true
		case "--version":
			options.version = true
		case "--no-play":
			if hasValue {
				return options, fmt.Errorf("%s does not take a value", name)
			}
			options.noPlay = true
		case "--export":
			options.exportAudio = true
			if hasValue {
				if value == "" {
					return options, fmt.Errorf("%s needs a directory after '='", name)
				}
				options.exportPath = value
			} else if len(positional) == 1 && i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-") {
				options.exportPath = args[i+1]
				i++
			}
		case "--translate":
			parsed, next, err := optionValue(args, i, name, value, hasValue)
			if err != nil {
				return options, err
			}
			i = next
			target, err := translate.NormalizeLanguageTag(parsed)
			if err != nil {
				return options, err
			}
			options.targetLanguage = target
		case "--voice":
			parsed, next, err := optionValue(args, i, name, value, hasValue)
			if err != nil {
				return options, err
			}
			i = next
			options.voice = parsed
			options.voiceExplicit = true
		case "--speed":
			parsed, next, err := floatOption(args, i, name, value, hasValue)
			if err != nil {
				return options, err
			}
			i = next
			options.speed = parsed
			options.speedExplicit = true
		case "--playback-speed":
			parsed, next, err := floatOption(args, i, name, value, hasValue)
			if err != nil {
				return options, err
			}
			i = next
			options.playbackSpeed = parsed
		default:
			if strings.HasPrefix(arg, "-") {
				return options, fmt.Errorf("unknown option %q", arg)
			}
			positional = append(positional, arg)
		}
	}
	if options.help || options.version {
		return options, nil
	}
	if len(positional) != 1 {
		return options, errors.New("provide exactly one text source")
	}
	options.source = positional[0]
	if options.voice == "" {
		return options, errors.New("voice ID cannot be empty")
	}
	if options.speed < 0.5 || options.speed > 2 || options.speed != options.speed {
		return options, errors.New("speech speed must be between 0.5 and 2.0")
	}
	if options.playbackSpeed < 0.5 || options.playbackSpeed > 3 || options.playbackSpeed != options.playbackSpeed {
		return options, errors.New("playback speed must be between 0.5 and 3.0")
	}
	return options, nil
}

func optionValue(args []string, index int, name, value string, hasValue bool) (string, int, error) {
	if hasValue {
		if value == "" {
			return "", index, fmt.Errorf("%s needs a value", name)
		}
		return value, index, nil
	}
	if index+1 >= len(args) || args[index+1] == "" {
		return "", index, fmt.Errorf("%s needs a value", name)
	}
	return args[index+1], index + 1, nil
}

func floatOption(args []string, index int, name, value string, hasValue bool) (float64, int, error) {
	textValue, next, err := optionValue(args, index, name, value, hasValue)
	if err != nil {
		return 0, index, err
	}
	parsed, err := strconv.ParseFloat(textValue, 64)
	if err != nil {
		return 0, index, fmt.Errorf("%s must be a number", name)
	}
	return parsed, next, nil
}

func listenSource(ctx context.Context, options listenOptions, stdin io.Reader, stderr io.Writer, deps runtimeDeps) error {
	if options.targetLanguage != "" && deps.translator != nil {
		if strings.TrimSpace(deps.translator.ID()) == "" {
			return apperr.New("translation_not_ready", "The local translation backend is not ready.", "Configure a local translator with a stable backend ID, or run listen without --translate.", nil)
		}
		capabilities, err := deps.translator.Capabilities(ctx)
		if err != nil {
			return apperr.New("translation_not_ready", "Could not inspect the translation backend.", "Check the local translator setup, or run listen without --translate.", err)
		}
		if !capabilities.Local {
			return apperr.New("translation_not_ready", "This translation backend cannot run locally.", "Remote translation requires explicit cloud permission; no source text was sent.", nil)
		}
	}
	userPaths, err := paths.FromEnv()
	if err != nil {
		return apperr.New("storage_unavailable", "Could not prepare local audio storage.", "Check your home directory and XDG paths.", err)
	}
	var localTranslatorErr error
	if options.targetLanguage != "" && deps.translator == nil {
		deps.translator, err = deps.startTranslator(ctx, userPaths)
		if err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			deps.translator = nil
			localTranslatorErr = err
		} else if deps.translator == nil {
			localTranslatorErr = errors.New("local translation worker did not start")
		}
		if deps.translator != nil {
			if closer, ok := deps.translator.(interface{ Close() error }); ok {
				defer closer.Close()
			}
		}
	}
	var translatorCapabilities translate.Capabilities
	if options.targetLanguage != "" && deps.translator != nil {
		if strings.TrimSpace(deps.translator.ID()) == "" {
			return apperr.New("translation_not_ready", "The local translation backend is not ready.", "Run listen setup --translate en:pt-BR, then run listen again.", nil)
		}
		translatorCapabilities, err = deps.translator.Capabilities(ctx)
		if err != nil {
			return apperr.New("translation_not_ready", "Could not inspect the local translation backend.", "Check local setup or run listen without --translate.", err)
		}
		if !translatorCapabilities.Local || translatorCapabilities.Remote {
			return apperr.New("translation_not_ready", "This translation backend cannot run locally.", "Remote translation requires explicit cloud permission; no source text was sent.", nil)
		}
	}
	var input source.Input
	if options.source == "-" {
		if stdin == nil {
			return apperr.New("source_unavailable", "Standard input is unavailable.", "Pass a text file path instead.", nil)
		}
		input = source.Input{Name: "stdin.txt", Path: "stdin", R: stdin}
	} else {
		path := expandHome(options.source)
		input = source.Input{Name: filepath.Base(path), Path: path}
	}
	readerConfig, _, err := settings.Load(userPaths.ConfigDir)
	if err != nil {
		return apperr.New("config_unavailable", "Could not read listen preferences.", "Run listen config to inspect or repair the local configuration.", err)
	}
	cloudConsentGranted := false
	if options.targetLanguage != "" && deps.translator == nil {
		switch readerConfig.CloudPolicy {
		case "never":
			return apperr.New("translation_not_ready", "Local translation is not ready.", "Run listen setup --translate en:pt-BR or change cloud policy before trying a cloud translator.", localTranslatorErr)
		case "ask":
			cloudConsentGranted, err = deps.confirmCloudFallback(stdin, stderr, "Local translation is unavailable. Allow OpenAI to receive this source's text for translation?")
			if err != nil {
				return apperr.New("cloud_consent_unavailable", "Could not ask for cloud translation permission.", "Run listen from an interactive terminal or install local translation.", err)
			}
			if !cloudConsentGranted {
				return apperr.New("translation_not_ready", "Local translation is not ready.", "Cloud translation was not approved. Install local translation or run listen from an interactive terminal to approve cloud use.", localTranslatorErr)
			}
		case "allowed":
			cloudConsentGranted = true
		}
	}
	if !options.voiceExplicit {
		if options.targetLanguage != "" {
			options.voice = readerConfig.Voices[options.targetLanguage]
			if options.voice == "" {
				if voice, ok := kokoro.DefaultVoice(options.targetLanguage); ok {
					options.voice = voice.ID
				} else {
					return apperr.New("voice_unavailable", "No local speech voice is available for the translated language.", "Choose a target language with a bundled local voice or pass --voice explicitly.", nil)
				}
			}
		} else {
			options.voice = readerConfig.Speech.DefaultVoice
		}
	}
	if !options.speedExplicit {
		options.speed = readerConfig.Speech.DefaultSpeed
	}
	manifests, err := cache.NewFileStore(userPaths.CacheDir)
	if err != nil {
		return apperr.New("storage_unavailable", "Could not prepare local audio storage.", "Check that your cache directory is writable.", err)
	}
	doc, err := parseCachedDocument(ctx, input, manifests)
	if err != nil {
		if ctx.Err() != nil {
			return interrupted(ctx.Err())
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			return apperr.New("source_missing", "Could not find the source file.", "Check the file path and try again.", err)
		case errors.Is(err, textsource.ErrBinarySource):
			return apperr.New("source_binary", "This file does not look like readable text.", "Use a UTF-8 text file for now.", err)
		case errors.Is(err, textsource.ErrInvalidUTF8):
			return apperr.New("source_encoding", "The source is not valid UTF-8 text.", "Save it as UTF-8 and run listen again.", err)
		case errors.Is(err, textsource.ErrEmptySource):
			if strings.EqualFold(filepath.Ext(input.Name), ".pdf") {
				return apperr.New("pdf_no_readable_text", "No readable text was found in this PDF.", "Choose a PDF with an extractable text layer; OCR is not available yet.", err)
			}
			return apperr.New("source_empty", "The source has no readable text.", "Choose a file that contains text.", err)
		default:
			if errors.Is(err, directorysource.ErrNoDocuments) {
				return apperr.New("source_empty", "This directory has no readable documentation files.", "Add text or Markdown documents, or check the directory's .gitignore rules.", err)
			}
			if strings.EqualFold(filepath.Ext(input.Name), ".pdf") {
				if errors.Is(err, pdfsource.ErrNeedsOCR) {
					return apperr.New("pdf_needs_ocr", "This PDF does not contain enough readable text.", "Choose a text-based PDF. Scanned-page OCR is not available yet.", err)
				}
				if errors.Is(err, pdfsource.ErrReaderUnavailable) {
					return apperr.New("pdf_reader_unavailable", "PDF reading is unavailable on this computer.", "Install Poppler utilities, then run listen again.", err)
				}
				if errors.Is(err, pdfsource.ErrResourceLimit) {
					return apperr.New("pdf_resource_limit", "This PDF exceeds the reader's processing limits.", "Split the PDF into smaller files and try again.", err)
				}
				return apperr.New("source_read_failed", "Could not read this PDF document.", "Check that the file is a valid, readable PDF and try again.", err)
			}
			if strings.EqualFold(filepath.Ext(input.Name), ".epub") {
				return apperr.New("source_read_failed", "Could not read this EPUB book.", "Check that the file is a valid EPUB and try again.", err)
			}
			if isHTMLPath(input.Name) {
				return apperr.New("source_read_failed", "Could not read this HTML document.", "Check that the file is readable HTML and try again.", err)
			}
			if info, statErr := os.Stat(input.Path); statErr == nil && info.IsDir() {
				return apperr.New("source_read_failed", "Could not read this documentation directory.", "Check that its files and .gitignore rules are readable, then try again.", err)
			}
			return apperr.New("source_read_failed", "Could not read the text source.", "Check that the file is readable and try again.", err)
		}
	}
	plan, err := planCachedSegments(ctx, doc, input.Path, manifests)
	if err != nil {
		return apperr.New("segment_failed", "Could not prepare this text for listening.", "Check the source file and try again.", err)
	}
	if len(plan.Segments) == 0 {
		return apperr.New("segment_failed", "This source has no readable audio.", "Choose a file that contains text to listen to.", nil)
	}
	if options.targetLanguage != "" {
		if deps.translator != nil {
			if err := validatePlanLanguagePair(plan, doc.Metadata.Language, options.targetLanguage, translatorCapabilities); err != nil {
				return apperr.New("translation_pair_unsupported", "Local translation does not support this source and target language pair.", "The installed local translator supports English to Brazilian Portuguese (en:pt-BR). Add the source language to the document or choose a supported pair.", err)
			}
			candidate := viability.Candidate{
				ID: deps.translator.ID(), Local: translatorCapabilities.Local,
				Installed: true, SupportsPair: true,
			}
			if _, err := viability.SelectAuto([]viability.Candidate{candidate}, options.noPlay || options.exportAudio); err != nil {
				return apperr.New("translation_not_ready", "No installed local translator supports this language pair.", "Run listen setup --translate en:pt-BR or run listen without --translate.", err)
			}
		} else if localTranslatorErr == nil {
			localTranslatorErr = errors.New("local translation is not installed")
		}
	}
	var translationCache *translate.Cache
	var translationContext translate.ContextArtifact
	translationBackend := ""
	streamTranslator := deps.translator
	planner := viability.NewPlanner(0.25)
	startupTranslationGenerated := 0
	playbackMode := !options.noPlay && !options.exportAudio
	terminal := ui.New(stderr)
	var translationWarningOnce sync.Once
	if options.targetLanguage != "" {
		translationCache, err = translate.NewCache(manifests)
		if err != nil {
			return apperr.New("storage_unavailable", "Could not prepare translation storage.", "Check that your cache directory is writable.", err)
		}
		translationContext, err = translate.NewContextArtifact(plan.DocumentID, doc.Metadata.Language, options.targetLanguage)
		if err != nil {
			return apperr.New("invalid_arguments", "The target language is not supported.", "Pass a valid BCP-47 language tag such as pt-BR.", err)
		}
		if _, err := translationCache.SaveContext(ctx, translationContext); err != nil {
			return apperr.New("storage_unavailable", "Could not save translation context.", "Check that your cache directory is writable.", err)
		}
		if deps.translator != nil {
			translationBackend = deps.translator.ID()
			if playbackMode {
				probe, probeErr := pipeline.ProbeTranslation(ctx, plan.Segments, deps.translator, translationCache, translationContext, 4)
				if probeErr != nil {
					if ctx.Err() != nil {
						return interrupted(ctx.Err())
					}
					return apperr.New("translation_failed", "Local translation could not prepare this session.", "Check the local translator setup, or run listen --export to finish without a playback deadline.", probeErr)
				}
				startupTranslationGenerated = len(probe.Translated)
				if startupTranslationGenerated > 0 {
					var estimatedAudio time.Duration
					for _, item := range probe.Translated {
						estimatedAudio += planner.EstimateSpeechDuration(item.SpokenText, item.TargetLang)
					}
					ratio := planner.ObserveTranslationDuration(probe.WallTime, estimatedAudio)
					selection, selectErr := viability.SelectAuto([]viability.Candidate{{
						ID: translationBackend, Local: true, Installed: true, SupportsPair: true, RealtimeRatio: ratio,
					}}, false)
					if selectErr != nil {
						return apperr.New("translation_not_ready", "No installed local translator supports this language pair.", "Run listen setup --translate en:pt-BR or run listen without --translate.", selectErr)
					}
					if selection.Warning != "" {
						translationWarningOnce.Do(func() { _ = terminal.Warning(selection.Warning) })
					}
					if ratio > 0 && ratio < viability.RiskyThreshold {
						cloud, approved, cloudErr := requestCloudTranslator(ctx, stdin, stderr, readerConfig, deps,
							"Local translation may fall behind playback. Allow OpenAI to receive this book's text for translation?")
						if cloudErr != nil {
							if readerConfig.CloudPolicy == "never" || !approved {
								return apperr.New("cloud_unavailable", "Could not prepare the configured cloud translator.", "Check the cloud provider settings and OPENAI_API_KEY; no source text was sent.", cloudErr)
							}
							_ = terminal.Warning("Cloud translation is unavailable; continuing with local translation.")
						} else if cloud != nil {
							streamTranslator = cloud
							translationBackend = cloud.ID()
							translatorCapabilities, _ = cloud.Capabilities(ctx)
							startupTranslationGenerated = 0
							if closer, ok := cloud.(interface{ Close() error }); ok {
								defer closer.Close()
							}
							_ = terminal.Info("Using the configured cloud translator for this session.")
						} else if !approved {
							_ = terminal.Warning("Continuing with local translation; no source text was sent to a cloud service.")
						}
					}
				}
			}
		} else {
			cloud, cloudErr := startAuthorizedCloudTranslator(ctx, readerConfig, deps, cloudConsentGranted)
			if cloudErr != nil {
				return apperr.New("cloud_unavailable", "Cloud translation is not ready.", "Check the cloud provider settings and OPENAI_API_KEY; no source text was sent.", cloudErr)
			}
			if cloud == nil {
				help := "Install local translation with listen setup --translate en:pt-BR, or set cloud policy to allowed and configure OPENAI_API_KEY."
				return apperr.New("translation_not_ready", "No authorized translation backend is available.", help, localTranslatorErr)
			}
			streamTranslator = cloud
			translationBackend = cloud.ID()
			translatorCapabilities, _ = cloud.Capabilities(ctx)
			if closer, ok := cloud.(interface{ Close() error }); ok {
				defer closer.Close()
			}
			_ = terminal.Info("Using the configured cloud translator for this session.")
		}
	}
	audioCache, err := speech.NewCache(userPaths.CacheDir)
	if err != nil {
		return apperr.New("storage_unavailable", "Could not prepare local audio storage.", "Check that your cache directory is writable.", err)
	}
	streamPlan := plan
	var resumeOffset time.Duration
	var sessionStore *session.Store
	if !options.noPlay && !options.exportAudio && options.source != "-" {
		sessionStore, err = session.NewStore(userPaths.CacheDir, input.Path)
		if err != nil {
			return apperr.New("storage_unavailable", "Could not prepare playback resume data.", "Check that your cache directory is writable.", err)
		}
		state, found, loadErr := sessionStore.Load(ctx)
		if loadErr != nil {
			_ = terminal.Warning("The saved playback position could not be read; starting at the beginning.")
		} else if found && !state.Complete {
			translationMatches := state.TargetLanguage == options.targetLanguage && state.TranslationBackend == translationBackend
			if translationMatches {
				point, ok := session.Resolve(plan, state)
				if ok {
					streamPlan.Segments = append([]segment.Segment(nil), plan.Segments[point.Index:]...)
					resumeOffset = point.Offset
					_ = terminal.Info("Resuming from your saved position.")
				}
			} else {
				_ = terminal.Info("Starting from the beginning because the translation settings changed.")
			}
		}
	}
	translationResolvedFromCache := false
	cachedTranslationCount := 0
	if translationCache != nil {
		translatedPlan, complete, err := translationCache.ResolveCachedPlan(ctx, streamPlan, translationBackend, translationContext)
		if err != nil {
			return apperr.New("storage_unavailable", "Could not read cached translations.", "Check that your cache directory is available.", err)
		}
		if complete {
			streamPlan = translatedPlan
			streamTranslator = nil
			translationResolvedFromCache = true
			cachedTranslationCount = len(translatedPlan.Segments)
		}
	}
	allCached := false
	if translationCache == nil || translationResolvedFromCache {
		allCached, err = allAudioCached(ctx, audioCache, streamPlan, options.voice, options.speed)
	}
	if err != nil {
		return apperr.New("storage_unavailable", "Could not read cached audio.", "Check that your cache directory is available.", err)
	}
	var bufferWarningOnce sync.Once
	var synthesizer speech.Synthesizer
	if allCached {
		synthesizer = cachedAudioSynthesizer{}
	} else {
		client, err := deps.startSynth(ctx, kokoro.Options{CacheRoot: userPaths.CacheDir, Voice: options.voice})
		if err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return apperr.New("speech_setup_required", "Local speech is not ready.", "Run listen setup to install the speech engine and voice.", err)
		}
		synthesizer = client
		defer synthesizer.Close()
	}

	var player playback.Player
	if !options.noPlay && !options.exportAudio {
		player = deps.newPlayer(playback.MPVOptions{PlaybackSpeed: options.playbackSpeed, Stdin: stdin, Stderr: stderr})
		defer player.Close()
	}
	var playbackMonitor *session.Monitor
	if player != nil && sessionStore != nil {
		baseState := session.State{
			DocumentID: plan.DocumentID, PlaybackSpeed: options.playbackSpeed, Voice: options.voice,
			TargetLanguage: options.targetLanguage, TranslationBackend: translationBackend,
		}
		if len(streamPlan.Segments) > 0 {
			if initial, ok := session.RecordPosition(baseState, plan, streamPlan.Segments[0].Seq, resumeOffset); ok {
				if err := sessionStore.Save(ctx, initial); err != nil {
					return apperr.New("session_save_failed", "Could not save the playback position.", "Check that your cache directory is writable.", err)
				}
				baseState = initial
			}
		}
		playbackMonitor = session.StartMonitor(ctx, player, sessionStore, baseState, plan, deps.sessionPollInterval)
		defer func() { _ = playbackMonitor.Stop() }()
	}
	progressStride := len(streamPlan.Segments) / 10
	if progressStride < 1 {
		progressStride = 1
	}
	streamOptions := pipeline.StreamOptions{
		Workers: 1, QueueCapacity: 2, StartupBuffer: deps.startupBuffer,
		Voice: options.voice, Speed: options.speed, ResumeOffset: resumeOffset,
		Translator: streamTranslator, TranslationCache: translationCache,
		TranslationContext: translationContext, TargetLanguage: options.targetLanguage,
		TranslationWorkers: 2, TranslationBatchSize: 4,
		OnProgress: func(completed, total int) {
			if completed%progressStride == 0 || completed == total {
				_ = terminal.Info(fmt.Sprintf("Audio ready: %d/%d segments", completed, total))
			}
		},
		OnPlaybackStart: func() { _ = terminal.Info("Playback started.") },
		OnTranslationMeasured: func(wall time.Duration, translated []segment.Segment) {
			measured := make([]viability.MeasuredText, 0, len(translated))
			for _, item := range translated {
				locale := item.TargetLang
				if locale == "" {
					locale = options.targetLanguage
				}
				measured = append(measured, viability.MeasuredText{Text: item.SpokenText, Locale: locale})
			}
			ratio := planner.ObserveTranslation(wall, measured)
			if playbackMode && translatorCapabilities.Local {
				selection, selectErr := viability.SelectAuto([]viability.Candidate{{
					ID: translationBackend, Local: translatorCapabilities.Local, Installed: true,
					SupportsPair: true, RealtimeRatio: ratio,
				}}, false)
				if selectErr == nil && selection.Warning != "" {
					translationWarningOnce.Do(func() { _ = terminal.Warning(selection.Warning) })
				}
			}
		},
		OnSpeechMeasured: func(item segment.Segment, wall, actual time.Duration) {
			locale := item.TargetLang
			text := item.SpokenText
			if locale == "" {
				locale = item.SourceLang
			}
			if text == "" {
				text = item.SourceText
			}
			planner.ObserveSpeech(text, locale, wall, actual)
		},
		OnBufferHealth: func(seconds float64, at time.Time) {
			planner.ObserveBuffer(at, seconds)
			snapshot := planner.Snapshot()
			if playbackMode && snapshot.PredictedUnderrun > 0 && snapshot.PredictedUnderrun <= 45*time.Second {
				bufferWarningOnce.Do(func() {
					_ = terminal.Warning(fmt.Sprintf("Ready audio may run out in about %s; playback can pause while local processing catches up.", snapshot.PredictedUnderrun.Round(time.Second)))
				})
			}
		},
	}
	report, err := pipeline.Stream(ctx, streamPlan, synthesizer, audioCache, player, streamOptions)
	if err != nil {
		if ctx.Err() != nil {
			return interrupted(ctx.Err())
		}
		return apperr.New("playback_failed", "Translation, audio generation, or playback stopped before the source finished.", "Completed work is safe; run listen again to resume.", err)
	}
	if startupTranslationGenerated > 0 {
		report.TranslationGenerated += startupTranslationGenerated
		report.TranslationReused -= startupTranslationGenerated
		if report.TranslationReused < 0 {
			report.TranslationReused = 0
		}
	}
	report.TranslationReused += cachedTranslationCount
	if options.exportAudio {
		outputDirectory := options.exportPath
		if outputDirectory == "" {
			outputDirectory = defaultExportDirectory(options.source, input.Path)
		} else {
			outputDirectory = expandHome(outputDirectory)
		}
		exportPlan := plan
		if translationCache != nil {
			var complete bool
			exportPlan, complete, err = translationCache.ResolveCachedPlan(ctx, plan, translationBackend, translationContext)
			if err != nil {
				return apperr.New("storage_unavailable", "Could not read completed translations for export.", "Check that your cache directory is available.", err)
			}
			if !complete {
				return apperr.New("playback_failed", "The translated source is incomplete.", "Run listen again to complete translation before exporting.", nil)
			}
		}
		result, err := audiobook.Build(ctx, audiobook.Options{
			Root: outputDirectory, SourcePath: input.Path, DocumentID: plan.DocumentID,
			TargetLanguage: options.targetLanguage, Title: doc.Metadata.Title, Authors: doc.Metadata.Authors,
			Voice: options.voice, Speed: options.speed, Plan: exportPlan, Audio: report.Results,
		})
		if err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return apperr.New("export_failed", "Could not create the audiobook export.", "Completed audio is safe; fix the reported storage or FFmpeg issue and run listen again.", err)
		}
		_ = terminal.Success("Audiobook is ready at " + result.Directory)
		return nil
	}
	if player != nil {
		if err := player.Wait(ctx); err != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return apperr.New("playback_failed", "Playback ended unexpectedly.", "Run listen again to resume from cached audio.", err)
		}
		if playbackMonitor != nil {
			if err := playbackMonitor.Stop(); err != nil {
				return apperr.New("session_save_failed", "Playback finished, but its last position could not be saved.", "Completed audio remains in the local cache.", err)
			}
		}
		if sessionStore != nil {
			last := plan.Segments[len(plan.Segments)-1]
			state := session.State{
				DocumentID: plan.DocumentID, SegmentID: last.ID, SegmentSeq: last.Seq,
				MajorUnitID: last.MajorUnitID, SectionPath: append([]string(nil), last.SectionPath...),
				PlaybackSpeed: options.playbackSpeed, Voice: options.voice, Complete: true,
				TargetLanguage: options.targetLanguage, TranslationBackend: translationBackend,
			}
			if len(report.Results) > 0 {
				state.Offset = report.Results[len(report.Results)-1].Duration
			}
			if err := sessionStore.Save(ctx, state); err != nil {
				return apperr.New("session_save_failed", "Playback finished, but its completion state could not be saved.", "Completed audio remains in the local cache.", err)
			}
		}
	} else {
		_ = terminal.Success(fmt.Sprintf("Audio is ready in the local cache (%d generated, %d reused).", report.Generated, report.Reused))
	}
	return nil
}

func validatePlanLanguagePair(plan segment.Plan, documentLanguage, target string, capabilities translate.Capabilities) error {
	if len(capabilities.LanguagePairs) == 0 {
		return nil
	}
	for _, item := range plan.Segments {
		source := item.SourceLang
		if strings.TrimSpace(source) == "" {
			source = documentLanguage
		}
		if !capabilities.SupportsLanguagePair(source, target) {
			pair, err := translate.LanguagePair(source, target)
			if err != nil {
				return fmt.Errorf("source language is not declared for segment %d", item.Seq)
			}
			return fmt.Errorf("installed translator does not support %s", pair)
		}
	}
	return nil
}

func requestCloudTranslator(
	ctx context.Context,
	stdin io.Reader,
	stderr io.Writer,
	config settings.Config,
	deps runtimeDeps,
	prompt string,
) (translate.Translator, bool, error) {
	approved := config.CloudPolicy == "allowed"
	if config.CloudPolicy == "ask" {
		var err error
		approved, err = deps.confirmCloudFallback(stdin, stderr, prompt)
		if err != nil {
			return nil, false, err
		}
	}
	if !approved {
		return nil, false, nil
	}
	translator, err := startAuthorizedCloudTranslator(ctx, config, deps, approved)
	return translator, true, err
}

func startAuthorizedCloudTranslator(ctx context.Context, config settings.Config, deps runtimeDeps, approved bool) (translate.Translator, error) {
	if !approved || config.CloudPolicy == "never" {
		return nil, nil
	}
	provider, err := deps.startCloudTranslator(ctx, config)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, errors.New("cloud translation provider did not start")
	}
	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		if closer, ok := provider.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	if !capabilities.Remote || capabilities.Local {
		if closer, ok := provider.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, errors.New("configured cloud provider is not a remote translation adapter")
	}
	authorized, err := translate.AuthorizeCloud(provider, config.CloudPolicy, approved)
	return authorized, err
}

func parseDocument(ctx context.Context, input source.Input) (*domain.Document, error) {
	parsers := []source.Parser{epubsource.NewParser(), htmlsource.NewParser(), pdfsource.NewParser(), markdownsource.NewParser(), textsource.NewParser()}
	allParsers := append([]source.Parser{directorysource.NewParser(parsers...)}, parsers...)
	selected, err := source.SelectParser(ctx, input, allParsers...)
	if err != nil {
		return nil, err
	}
	return selected.Parse(ctx, input)
}

func writeUsage(stdout io.Writer) error {
	if stdout == nil {
		return fmt.Errorf("write help: output writer is nil")
	}
	_, err := io.WriteString(stdout, usage)
	return err
}

func interrupted(cause error) error {
	return apperr.New("interrupted", "Listening was interrupted.", "Completed audio is safe; run listen again to resume.", cause)
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func isHTMLPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm", ".xhtml":
		return true
	default:
		return false
	}
}
