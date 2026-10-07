package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/prettyletto/listen/internal/catalog"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

type pendingHomeSource struct {
	options    listenOptions
	direct     bool
	relinkFrom string
}

type homeOperation uint8

const (
	homeSetupOperation homeOperation = iota + 1
	homePreviewOperation
)

type homeOperationResult struct {
	kind homeOperation
	err  error
}

// runListeningHome owns the interactive application lifetime. The home and
// playback screens share one Bubble Tea program; source processing remains in
// the existing app pipeline.
func runListeningHome(ctx context.Context, initial *listenOptions, stdin io.Reader, stderr io.Writer, deps runtimeDeps) error {
	userPaths, pathErr := paths.FromEnv()
	var library *catalog.Catalog
	var entries []catalog.Entry
	note := ""
	if pathErr != nil {
		note = "Your library could not be opened. You can still listen by entering a path."
	} else {
		library = catalog.New(userPaths.DataDir)
		var err error
		entries, err = library.List(ctx)
		if err != nil {
			note = "Your library could not be read. Listening still works; recent sources may not be saved."
			entries = nil
		}
	}
	controller, err := playerui.StartHome(stdin, stderr, homeModel(entries, note))
	if err != nil {
		return err
	}
	defer controller.Shutdown()
	deps.startPlaybackScreen = func(_ io.Reader, _ io.Writer, model playerui.Model) (playerScreen, error) {
		return controller.ShowPlayback(model)
	}
	var playbackEnd PlaybackEnd
	deps.playbackEnd = &playbackEnd
	operationDone := make(chan homeOperationResult, 1)
	var pending *pendingHomeSource
	var operation homeOperation
	var cancelOperation context.CancelFunc
	var exitAfterOperation bool
	var setupOnly bool
	var setupReturnPreferences bool

	runDirect := func(options listenOptions) (bool, error) {
		note, _, playErr := playFromHome(ctx, options, stdin, stderr, deps, library, controller, &entries, &playbackEnd, true)
		if playErr != nil {
			if playbackEnd == PlaybackReturnedHome && ctx.Err() == nil {
				if refreshed, refreshErr := libraryEntries(ctx, library); refreshErr == nil {
					entries = refreshed
				}
				controller.SetHome(homeEntries(entries), note)
				return false, nil
			}
			return true, playErr
		}
		if playbackEnd != PlaybackReturnedHome {
			controller.Shutdown()
			if homeCatalogFailure(note) {
				_, _ = fmt.Fprintln(stderr, note)
			}
			return true, nil
		}
		if refreshed, refreshErr := libraryEntries(ctx, library); refreshErr == nil {
			entries = refreshed
		}
		controller.SetHome(homeEntries(entries), note)
		return false, nil
	}

	openPreferences := func(returnTo playerui.Phase) {
		preferencePaths, err := paths.FromEnv()
		if err != nil {
			controller.SetHome(homeEntries(entries), "Could not locate listening preferences. Check your XDG configuration directory.")
			return
		}
		config, _, err := settings.Load(preferencePaths.ConfigDir)
		if err != nil {
			controller.SetHome(homeEntries(entries), "Could not read listening preferences. Check the local config file and try again.")
			return
		}
		preferences := preferencesForConfig(config)
		needsSetup, readinessErr := speechSetupNeeded(ctx, deps, preferencePaths)
		if readinessErr != nil {
			preferences.SetupReady = false
			preferences.SetupStatus = "Could not check local speech readiness. Setup can repair it."
		} else if needsSetup {
			preferences.SetupReady = false
			preferences.SetupStatus = "Local speech needs setup or repair on this device."
		} else {
			preferences.SetupReady = true
			preferences.SetupStatus = "Local speech is ready on this device."
		}
		controller.SetPreferences(preferences, returnTo)
	}

	showSetupChoice := func(selection pendingHomeSource, message string) {
		pendingCopy := selection
		pending = &pendingCopy
		controller.SetSetupChoice(selection.options.source, message)
	}

	if initial != nil {
		needsSetup, checkErr := selectedSourceNeedsSetup(ctx, deps, userPaths, *initial)
		if checkErr != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return checkErr
		}
		if needsSetup {
			showSetupChoice(pendingHomeSource{options: *initial, direct: true}, "This source is selected. Local speech needs preparation before playback can begin.")
		} else {
			shouldExit, playErr := runDirect(*initial)
			if playErr != nil {
				return playErr
			}
			if shouldExit {
				return nil
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			if cancelOperation != nil {
				cancelOperation()
				<-operationDone
			}
			return interrupted(ctx.Err())
		case result := <-operationDone:
			if cancelOperation != nil {
				cancelOperation()
				cancelOperation = nil
			}
			finished := operation
			operation = 0
			if exitAfterOperation {
				controller.Shutdown()
				return nil
			}
			if finished == homeSetupOperation {
				if result.err != nil {
					message := "Setup stopped. Your selected source is still here; press Enter to retry."
					if !errors.Is(result.err, context.Canceled) {
						message = "Setup could not finish. Check your connection and storage, then press Enter to retry."
					}
					if pending != nil {
						controller.SetSetupChoice(pending.options.source, message)
					} else {
						controller.SetSetupChoice("", message)
					}
					continue
				}
				if pending == nil {
					setupOnly = false
					setupReturnPreferences = false
					controller.SetHome(homeEntries(entries), "Local speech is ready.")
					continue
				}
				selected := *pending
				pending = nil
				if selected.direct {
					shouldExit, playErr := runDirect(selected.options)
					if playErr != nil {
						return playErr
					}
					if shouldExit {
						return nil
					}
					continue
				}
				controller.SetHome(homeEntries(entries), "Local speech is ready. Opening your selected source…")
				if selected.relinkFrom != "" {
					controller.QueueAction(playerui.Action{Kind: playerui.ActionRelinkSource, Path: selected.options.source, RelinkFrom: selected.relinkFrom})
				} else {
					controller.QueueAction(playerui.Action{Kind: playerui.ActionOpenSource, Path: selected.options.source})
				}
				continue
			}
			if finished == homePreviewOperation {
				message := "Voice preview stopped."
				if result.err == nil {
					message = "Voice preview finished."
				} else if !errors.Is(result.err, context.Canceled) {
					message = "Could not play the local preview. Check setup and your audio output."
				}
				controller.FinishVoicePreview(message)
			}
		case action, ok := <-controller.Actions():
			if !ok {
				return nil
			}
			switch action.Kind {
			case playerui.ActionExit, playerui.ActionStop:
				return nil
			case playerui.ActionOpenPreferences:
				openPreferences(playerui.PhaseHome)
			case playerui.ActionOpenSetup:
				wantsSettingsSetup := action.Preferences.DefaultVoice != ""
				setupReturnPreferences = wantsSettingsSetup && pending == nil
				if wantsSettingsSetup {
					preferencePaths, pathErr := paths.FromEnv()
					if pathErr != nil {
						controller.SetPreferenceNote("Could not locate preferences. Check your XDG configuration directory.")
						continue
					}
					config, _, loadErr := settings.Load(preferencePaths.ConfigDir)
					if loadErr != nil || applyPreferences(&config, action.Preferences) != nil {
						controller.SetPreferenceNote("Could not validate those changes. Review them before starting setup.")
						continue
					}
					if saveErr := settings.Save(context.WithoutCancel(ctx), preferencePaths.ConfigDir, config); saveErr != nil {
						controller.SetPreferenceNote("Could not save preferences. Check that your config directory is writable.")
						continue
					}
				}
				if pending != nil {
					setupOnly = false
					controller.SetSetupChoice(pending.options.source, "Preferences saved. Start setup when you are ready.")
				} else {
					setupOnly = true
					controller.SetSetupChoice("", "")
				}
			case playerui.ActionSavePreferences:
				preferencePaths, pathErr := paths.FromEnv()
				if pathErr != nil {
					controller.SetPreferenceNote("Could not locate preferences. Check your XDG configuration directory.")
					continue
				}
				config, _, loadErr := settings.Load(preferencePaths.ConfigDir)
				if loadErr != nil {
					controller.SetPreferenceNote("Could not read preferences. Check the local config file and try again.")
					continue
				}
				if saveErr := applyPreferences(&config, action.Preferences); saveErr != nil {
					controller.SetPreferenceNote("Those voice or speed choices are unavailable. Review them and try again.")
					continue
				}
				if saveErr := settings.Save(context.WithoutCancel(ctx), preferencePaths.ConfigDir, config); saveErr != nil {
					controller.SetPreferenceNote("Could not save preferences. Check that your config directory is writable.")
					continue
				}
				if pending != nil {
					controller.SetSetupChoice(pending.options.source, "Preferences saved. Press Enter when you are ready to download local speech.")
				} else if setupOnly {
					controller.SetSetupChoice("", "Preferences saved. Start setup when you are ready.")
				} else {
					controller.SetHome(homeEntries(entries), "Listening preferences saved for your next session.")
				}
			case playerui.ActionCustomizeSetup:
				openPreferences(playerui.PhaseSetupChoice)
			case playerui.ActionStartRecommendedSetup:
				if operation != 0 {
					controller.SetSetupChoice("", "Local setup is already running.")
					continue
				}
				preferencePaths, pathErr := paths.FromEnv()
				if pathErr != nil {
					controller.SetSetupChoice(pending.options.source, "Could not locate settings and local storage. Check your XDG directories.")
					continue
				}
				config, _, loadErr := settings.Load(preferencePaths.ConfigDir)
				if loadErr != nil {
					controller.SetSetupChoice(pending.options.source, "Could not read local voice preferences. Check the config file before setup.")
					continue
				}
				opCtx, cancel := context.WithCancel(ctx)
				cancelOperation = cancel
				operation = homeSetupOperation
				exitAfterOperation = false
				setupSource := ""
				if pending != nil {
					setupSource = pending.options.source
				}
				controller.SetSetupProgress(setupSource, "Downloading local speech files…")
				go func() {
					setupErr := prepareLocalSpeechRuntime(opCtx, preferencePaths, config, io.Discard, io.Discard, deps, func(note string) {
						controller.SetSetupProgress(setupSource, note)
					})
					if setupErr == nil {
						config.SetupComplete = true
						setupErr = settings.Save(opCtx, preferencePaths.ConfigDir, config)
					}
					operationDone <- homeOperationResult{kind: homeSetupOperation, err: setupErr}
				}()
			case playerui.ActionCancelSetup:
				if operation == homeSetupOperation && cancelOperation != nil {
					exitAfterOperation = action.ExitAfterCancel
					cancelOperation()
				} else {
					pending = nil
					if setupReturnPreferences {
						setupReturnPreferences = false
						setupOnly = false
						openPreferences(playerui.PhaseHome)
					} else {
						setupOnly = false
						controller.SetHome(homeEntries(entries), "Setup was not started.")
					}
				}
			case playerui.ActionPreviewVoice:
				if operation != 0 {
					controller.FinishVoicePreview("Another local operation is still running.")
					continue
				}
				preferencePaths, pathErr := paths.FromEnv()
				if pathErr != nil {
					controller.FinishVoicePreview("Could not locate local speech storage.")
					continue
				}
				config, _, loadErr := settings.Load(preferencePaths.ConfigDir)
				needsSetup, readinessErr := speechSetupNeeded(ctx, deps, preferencePaths)
				if readinessErr != nil {
					if ctx.Err() != nil {
						return interrupted(ctx.Err())
					}
					controller.FinishVoicePreview("Could not check local speech readiness.")
					continue
				}
				if loadErr != nil || needsSetup {
					controller.FinishVoicePreview("Start recommended setup before previewing a local voice.")
					continue
				}
				opCtx, cancel := context.WithCancel(ctx)
				cancelOperation = cancel
				operation = homePreviewOperation
				exitAfterOperation = false
				go func() {
					previewErr := playVoicePreview(opCtx, preferencePaths, action.VoiceID, config.Speech.DefaultSpeed, config.PlaybackRate, deps)
					operationDone <- homeOperationResult{kind: homePreviewOperation, err: previewErr}
				}()
			case playerui.ActionCancelPreview:
				if operation == homePreviewOperation && cancelOperation != nil {
					exitAfterOperation = action.ExitAfterCancel
					cancelOperation()
				} else if action.ExitAfterCancel {
					controller.Shutdown()
					return nil
				} else {
					controller.FinishVoicePreview("Voice preview stopped.")
				}
			case playerui.ActionBrowseDirectory:
				absolute, browserEntries, browseNote := readDirectory(action.Path, action.ShowHidden)
				controller.SetBrowser(absolute, browserEntries, action.ShowHidden, browseNote)
			case playerui.ActionRemoveSource:
				if library == nil {
					controller.SetHome(homeEntries(entries), "Your listening home is unavailable, so this source could not be removed.")
					continue
				}
				removed, found, removeErr := library.Remove(context.WithoutCancel(ctx), action.Path)
				if removeErr != nil {
					controller.SetHome(homeEntries(entries), "Could not remove this source from home. Check that local storage is writable.")
					continue
				}
				if !found {
					controller.SetHome(homeEntries(entries), "This source is no longer in your listening home.")
					continue
				}
				entries = removeCatalogEntry(entries, removed.Path)
				undo := catalogEntryForUI(removed)
				controller.SetHomeWithUndo(homeEntries(entries), "Removed from home. Press u to undo.", &undo)
			case playerui.ActionUndoRemove:
				if library == nil {
					controller.SetHomeWithUndo(homeEntries(entries), "Your listening home is unavailable, so this entry could not be restored.", &action.Entry)
					continue
				}
				entry := catalog.Entry{
					Path: action.Entry.Path, Title: action.Entry.Title,
					TargetLanguage: action.Entry.TargetLanguage, CodeMode: action.Entry.CodeMode,
					ResumeLabel: action.Entry.ResumeLabel, LastOpenedAt: action.Entry.UpdatedAt,
					Complete: action.Entry.Complete,
				}
				restored, restoreErr := library.Upsert(context.WithoutCancel(ctx), entry)
				if restoreErr != nil {
					controller.SetHomeWithUndo(homeEntries(entries), "Could not restore the entry. Check that local storage is writable.", &action.Entry)
					continue
				}
				entries = upsertCatalogEntry(entries, restored)
				controller.SetHome(homeEntries(entries), "Source restored to your listening home.")
			case playerui.ActionOpenSource:
				options, parseErr := parseListenOptions([]string{action.Path})
				if parseErr != nil {
					controller.SetHome(homeEntries(entries), "Could not use that path: "+parseErr.Error())
					continue
				}
				needsSetup, readinessErr := selectedSourceNeedsSetup(ctx, deps, userPaths, options)
				if readinessErr != nil {
					if ctx.Err() != nil {
						return interrupted(ctx.Err())
					}
					controller.SetAddPath(action.Path, userFacingHomeError(readinessErr))
					continue
				}
				if needsSetup && options.source != "-" {
					showSetupChoice(pendingHomeSource{options: options}, "This source is selected. Local speech needs preparation before playback can begin.")
					continue
				}
				playbackEnd = PlaybackCompleted
				note, _, err := playFromHome(ctx, options, stdin, stderr, deps, library, controller, &entries, &playbackEnd, false)
				if err != nil {
					if ctx.Err() != nil {
						return interrupted(ctx.Err())
					}
					controller.SetHome(homeEntries(entries), userFacingHomeError(err))
					continue
				}
				if playbackEnd == PlaybackStopped {
					controller.Shutdown()
					if homeCatalogFailure(note) {
						_, _ = fmt.Fprintln(stderr, note)
					}
					return nil
				}
				if refreshed, refreshErr := libraryEntries(ctx, library); refreshErr == nil {
					entries = refreshed
				}
				controller.SetHome(homeEntries(entries), note)
			case playerui.ActionRelinkSource:
				options, parseErr := parseListenOptions([]string{action.Path})
				if parseErr != nil {
					controller.SetHome(homeEntries(entries), "Could not use that path: "+parseErr.Error())
					continue
				}
				oldEntry, hasOld := catalogEntry(entries, action.RelinkFrom)
				if hasOld {
					options.targetLanguage = oldEntry.TargetLanguage
					options.code = oldEntry.CodeMode
				}
				needsSetup, readinessErr := selectedSourceNeedsSetup(ctx, deps, userPaths, options)
				if readinessErr != nil {
					if ctx.Err() != nil {
						return interrupted(ctx.Err())
					}
					controller.SetHome(homeEntries(entries), userFacingHomeError(readinessErr))
					continue
				}
				if needsSetup && options.source != "-" {
					showSetupChoice(pendingHomeSource{options: options, relinkFrom: action.RelinkFrom}, "Replacement selected. Local speech needs preparation before playback can begin.")
					continue
				}
				transferWarning := ""
				if library != nil && hasOld && readingOptionsMatch(oldEntry, options) {
					if transferErr := transferCheckpoint(ctx, userPaths.CacheDir, oldEntry.Path, action.Path); transferErr != nil {
						transferWarning = "The saved place could not be transferred; this source will start from the beginning."
					}
				}
				playbackEnd = PlaybackCompleted
				note, catalogSaved, err := playFromHome(ctx, options, stdin, stderr, deps, library, controller, &entries, &playbackEnd, false)
				if err != nil {
					if ctx.Err() != nil {
						return interrupted(ctx.Err())
					}
					controller.SetHome(homeEntries(entries), userFacingHomeError(err))
					continue
				}
				if transferWarning != "" {
					note = transferWarning + " " + note
				}
				if hasOld {
					entries, _, _ = removeRelinkedCatalogEntry(context.WithoutCancel(ctx), library, entries, oldEntry.Path, options.source, catalogSaved)
				}
				if playbackEnd == PlaybackStopped {
					controller.Shutdown()
					if homeCatalogFailure(note) {
						_, _ = fmt.Fprintln(stderr, note)
					}
					return nil
				}
				if refreshed, refreshErr := libraryEntries(ctx, library); refreshErr == nil {
					entries = refreshed
				}
				controller.SetHome(homeEntries(entries), relinkHomeNote(note, catalogSaved, hasOld, oldEntry))
			}
		}
	}
}

func playFromHome(ctx context.Context, options listenOptions, stdin io.Reader, stderr io.Writer, deps runtimeDeps, library *catalog.Catalog, controller *playerui.Session, entries *[]catalog.Entry, end *PlaybackEnd, direct bool) (string, bool, error) {
	path := expandHome(options.source)
	if path != "-" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", false, err
		}
		options.source = filepath.Clean(absolute)
		for _, entry := range *entries {
			if entry.Path == options.source {
				if options.targetLanguage == "" && entry.TargetLanguage != "" {
					options.targetLanguage = entry.TargetLanguage
				}
				if entry.CodeMode {
					options.code = true
				}
				break
			}
		}
	}
	controller.SetHome(homeEntries(*entries), "Opening "+filepath.Base(options.source)+"…")
	*end = PlaybackCompleted
	deps.playbackEnd = end
	err := listenSource(ctx, options, stdin, stderr, deps)
	if err != nil {
		return "", false, err
	}
	note := completionNote(*end)
	catalogSaved := false
	if options.source != "-" && !options.noPlay && !options.exportAudio && library != nil {
		entry := catalog.Entry{
			Path: options.source, Title: sourceTitle(options.source),
			TargetLanguage: options.targetLanguage, CodeMode: options.code,
			LastOpenedAt: time.Now().UTC(),
		}
		if *end == PlaybackStopped || *end == PlaybackReturnedHome {
			cacheRoot := ""
			if userPaths, pathErr := paths.FromEnv(); pathErr == nil {
				cacheRoot = userPaths.CacheDir
			}
			entry.ResumeLabel = checkpointLabel(cacheRoot, options.source)
		} else {
			entry.Complete = true
		}
		if saved, saveErr := library.Upsert(context.WithoutCancel(ctx), entry); saveErr == nil {
			catalogSaved = true
			updated := false
			for index := range *entries {
				if (*entries)[index].Path == saved.Path {
					(*entries)[index] = saved
					updated = true
					break
				}
			}
			if !updated {
				*entries = append(*entries, saved)
			}
		} else {
			if *end == PlaybackStopped || *end == PlaybackReturnedHome {
				note = "Your playback position is saved, but this source could not be added to the listening home. Check that local storage is writable."
			} else {
				note = "Finished listening, but this source could not be added to the listening home. Check that local storage is writable."
			}
		}
	} else if options.source != "-" && !options.noPlay && !options.exportAudio {
		note = "Playback finished, but the local listening home is unavailable."
	}
	if direct && *end == PlaybackCompleted && library == nil {
		note = "Finished listening, but the local listening home is unavailable."
	}
	return note, catalogSaved, nil
}

func libraryEntries(ctx context.Context, library *catalog.Catalog) ([]catalog.Entry, error) {
	if library == nil {
		return nil, errors.New("local library is unavailable")
	}
	return library.List(ctx)
}

func homeEntries(entries []catalog.Entry) []playerui.LibraryEntry {
	items := make([]playerui.LibraryEntry, 0, len(entries))
	for _, entry := range entries {
		_, statErr := os.Stat(entry.Path)
		title := homeSourceTitle(entry.Title, entry.Path)
		resumeLabel := entry.ResumeLabel
		if bookLevelCheckpoint(resumeLabel, title) {
			resumeLabel = "Saved place"
		}
		items = append(items, playerui.LibraryEntry{
			Path: entry.Path, Title: title, TargetLanguage: entry.TargetLanguage,
			CodeMode: entry.CodeMode, ResumeLabel: resumeLabel,
			UpdatedAt: entry.LastOpenedAt, Complete: entry.Complete, Missing: errors.Is(statErr, os.ErrNotExist),
		})
	}
	return items
}

func homeModel(entries []catalog.Entry, note string) playerui.Model {
	return playerui.NewHome(homeEntries(entries), note)
}

func sourceTitle(path string) string {
	base := filepath.Base(path)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return base + "/"
	}
	return cleanArchiveTitle(strings.TrimSuffix(base, filepath.Ext(base)))
}

// libgenTitlePattern recognizes the distinctive filename shape used by one
// common ebook archive. It deliberately requires author, year/publisher, and
// identifier groups together so ordinary braces or parenthesized titles stay
// untouched.
var libgenTitlePattern = regexp.MustCompile(`(?i)^(.+?)\{[^{}]+\}\(\d{4},[^()]*\)\{[^{}]+\}\s+libgen\.[A-Za-z]{2,}$`)

func cleanArchiveTitle(title string) string {
	title = strings.TrimSpace(title)
	if matches := libgenTitlePattern.FindStringSubmatch(title); len(matches) == 2 {
		if clean := strings.TrimSpace(matches[1]); clean != "" {
			return clean
		}
	}
	return title
}

// Older catalog entries stored the filename stem as their title. Reformat
// that value for display while preserving parser-supplied document titles.
func homeSourceTitle(storedTitle, path string) string {
	title := strings.TrimSpace(storedTitle)
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if title == "" || title == base || title == stem {
		return sourceTitle(path)
	}
	return cleanArchiveTitle(title)
}

func bookLevelCheckpoint(label, title string) bool {
	label = strings.TrimSpace(label)
	title = strings.TrimSpace(title)
	if label == "" || title == "" {
		return false
	}
	if strings.EqualFold(label, title) {
		return true
	}
	labelRunes, titleRunes := []rune(label), []rune(title)
	if len(labelRunes) <= len(titleRunes) || !strings.EqualFold(string(labelRunes[:len(titleRunes)]), title) {
		return false
	}
	suffix := strings.TrimSpace(string(labelRunes[len(titleRunes):]))
	// EPUB metadata may append a parenthetical "for <reader>" attribution to
	// the document title. If that entire value was saved as a location, use
	// generic copy rather than calling the book itself a chapter. Other titles
	// and parenthetical chapter names remain intact.
	lowerSuffix := strings.ToLower(suffix)
	return strings.HasPrefix(lowerSuffix, "(for ") && strings.HasSuffix(suffix, ")")
}

func userFacingHomeError(err error) string {
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "Could not open that source. Check the path and try again."
	}
	return fmt.Sprintf("%s  ·  Press a to try another path.", message)
}

func completionNote(end PlaybackEnd) string {
	if end == PlaybackReturnedHome || end == PlaybackStopped {
		return "Your place is saved. Select it to continue."
	}
	return "Finished listening. Select a source to listen again."
}

func speechSetupNeeded(ctx context.Context, deps runtimeDeps, userPaths paths.Paths) (bool, error) {
	if userPaths.ConfigDir == "" || userPaths.DataDir == "" || userPaths.CacheDir == "" {
		return false, nil
	}
	if deps.speechReady == nil {
		return true, nil
	}
	ready, err := deps.speechReady(ctx, userPaths)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return true, nil
	}
	return !ready, nil
}

func preferencesForConfig(config settings.Config) playerui.Preferences {
	voices := make(map[string]string, len(config.Voices)+1)
	for locale, voice := range config.Voices {
		voices[locale] = voice
	}
	if defaultVoice, ok := kokoro.FindVoice(config.Speech.DefaultVoice); ok && voices[defaultVoice.Locale] == "" {
		voices[defaultVoice.Locale] = defaultVoice.ID
	}
	locales := make([]playerui.VoiceChoice, 0)
	for _, locale := range kokoro.Locales() {
		locales = append(locales, playerui.VoiceChoice{Locale: locale.Locale, LocaleName: locale.LocaleName})
	}
	options := make([]playerui.VoiceChoice, 0)
	for _, voice := range kokoro.Voices() {
		options = append(options, playerui.VoiceChoice{ID: voice.ID, Name: voice.Name, Locale: voice.Locale, LocaleName: voice.LocaleName})
	}
	return playerui.Preferences{
		DefaultVoice: config.Speech.DefaultVoice, SpeechSpeed: config.Speech.DefaultSpeed,
		PlaybackRate: config.PlaybackRate, OCRLanguages: config.OCRLanguages, CloudPolicy: config.CloudPolicy,
		SetupReady: config.SetupComplete, SetupStatus: "Local speech setup status is unknown.",
		Voices: voices, Locales: locales, VoiceOptions: options,
	}
}

func applyPreferences(config *settings.Config, preferences playerui.Preferences) error {
	if config == nil {
		return errors.New("preferences configuration is unavailable")
	}
	if preferences.SpeechSpeed < 0.5 || preferences.SpeechSpeed > 2 || preferences.SpeechSpeed != preferences.SpeechSpeed {
		return errors.New("speech generation speed is out of range")
	}
	if preferences.PlaybackRate < 0.5 || preferences.PlaybackRate > 3 || preferences.PlaybackRate != preferences.PlaybackRate {
		return errors.New("playback speed is out of range")
	}
	if err := settings.SetDefaultVoice(config, preferences.DefaultVoice); err != nil {
		return err
	}
	voiceMap := make(map[string]string, len(preferences.Voices)+1)
	for locale, voiceID := range preferences.Voices {
		if voiceID == "" {
			continue
		}
		if err := settings.SetLocaleVoice(config, locale, voiceID); err != nil {
			return err
		}
		voiceMap[locale] = voiceID
	}
	defaultVoice, _ := kokoro.FindVoice(preferences.DefaultVoice)
	voiceMap[defaultVoice.Locale] = defaultVoice.ID
	config.Voices = voiceMap
	config.Speech.DefaultSpeed = preferences.SpeechSpeed
	config.PlaybackRate = preferences.PlaybackRate
	if preferences.OCRLanguages != "" {
		config.OCRLanguages = preferences.OCRLanguages
	}
	if preferences.CloudPolicy != "" {
		config.CloudPolicy = preferences.CloudPolicy
	}
	return settings.Validate(*config)
}

func playVoicePreview(ctx context.Context, userPaths paths.Paths, voiceID string, speechSpeed, playbackRate float64, deps runtimeDeps) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if voiceID == "" {
		return errors.New("a voice must be selected for preview")
	}
	if _, ok := kokoro.FindVoice(voiceID); !ok {
		return fmt.Errorf("unknown preview voice %q", voiceID)
	}
	if speechSpeed < 0.5 || speechSpeed > 2 {
		speechSpeed = 1
	}
	if playbackRate < 0.5 || playbackRate > 3 {
		playbackRate = 1
	}
	if err := os.MkdirAll(userPaths.CacheDir, 0o700); err != nil {
		return fmt.Errorf("prepare local preview storage: %w", err)
	}
	previewDir, err := os.MkdirTemp(userPaths.CacheDir, "voice-preview-")
	if err != nil {
		return fmt.Errorf("create local preview workspace: %w", err)
	}
	defer os.RemoveAll(previewDir)
	output := filepath.Join(previewDir, "sample.wav")
	synthesizer, err := deps.startSynth(ctx, kokoro.Options{CacheRoot: userPaths.CacheDir, Voice: voiceID, Stderr: io.Discard})
	if err != nil {
		return err
	}
	if synthesizer == nil {
		return errors.New("local preview speech worker did not start")
	}
	defer func() {
		if closeErr := synthesizer.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	const previewSegmentID = "listen-voice-preview"
	result, err := synthesizer.Synthesize(ctx, speech.Request{
		Segment: segment.Segment{ID: previewSegmentID, Kind: segment.KindProse, SourceText: "A short sample to help you choose a listening voice.", SpokenText: "A short sample to help you choose a listening voice."},
		Voice:   voiceID, Speed: speechSpeed, Output: output,
	})
	if err != nil {
		return err
	}
	if result.SegmentID != previewSegmentID || result.Path != output || result.Duration <= 0 {
		return errors.New("local preview speech worker returned an invalid sample")
	}
	if _, err := speech.ValidateWAV(output); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	player := deps.newPlayer(playback.MPVOptions{PlaybackSpeed: playbackRate, Stderr: io.Discard})
	if player == nil {
		return errors.New("local preview player is unavailable")
	}
	defer func() {
		if closeErr := player.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if err := player.Start(ctx); err != nil {
		return err
	}
	if err := player.Append(ctx, playback.Item{Seq: 0, Path: result.Path, Duration: result.Duration, Title: "Voice preview"}); err != nil {
		return err
	}
	return player.Wait(ctx)
}

func readDirectory(path string, includeHidden bool) (string, []playerui.BrowserEntry, string) {
	if path == "" {
		path = "."
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path, nil, "Could not resolve this folder. Enter a different path or go back."
	}
	absolute = filepath.Clean(absolute)
	items, err := os.ReadDir(absolute)
	if err != nil {
		return absolute, nil, "Could not open this folder. Check its permissions or choose another folder."
	}
	entries := make([]playerui.BrowserEntry, 0, len(items))
	for _, item := range items {
		name := item.Name()
		if !includeHidden && strings.HasPrefix(name, ".") {
			continue
		}
		fullPath := filepath.Join(absolute, name)
		info, statErr := os.Stat(fullPath)
		if statErr != nil {
			continue
		}
		entries = append(entries, playerui.BrowserEntry{
			Name: name, Path: fullPath, Directory: info.IsDir(),
		})
	}
	// ReadDir is name-sorted. Present folders first so navigation is easy to
	// scan, retaining alphabetical order within each group.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return absolute, entries, ""
}

func catalogEntry(entries []catalog.Entry, path string) (catalog.Entry, bool) {
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return catalog.Entry{}, false
	}
	cleaned = filepath.Clean(cleaned)
	for _, entry := range entries {
		if entry.Path == cleaned {
			return entry, true
		}
	}
	return catalog.Entry{}, false
}

func catalogEntryForUI(entry catalog.Entry) playerui.LibraryEntry {
	return playerui.LibraryEntry{
		Path: entry.Path, Title: entry.Title, TargetLanguage: entry.TargetLanguage,
		CodeMode: entry.CodeMode, ResumeLabel: entry.ResumeLabel,
		UpdatedAt: entry.LastOpenedAt, Complete: entry.Complete,
	}
}

func removeCatalogEntry(entries []catalog.Entry, path string) []catalog.Entry {
	result := make([]catalog.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.Path != path {
			result = append(result, entry)
		}
	}
	return result
}

// removeRelinkedCatalogEntry drops the vanished path only after the replacement
// has been durably added. A failed catalog save must leave the old recovery
// entry available for another attempt.
func removeRelinkedCatalogEntry(ctx context.Context, library *catalog.Catalog, entries []catalog.Entry, oldPath, newPath string, replacementSaved bool) ([]catalog.Entry, bool, error) {
	if !replacementSaved || library == nil || filepath.Clean(oldPath) == filepath.Clean(newPath) {
		return entries, false, nil
	}
	removed, found, err := library.Remove(ctx, oldPath)
	if err != nil || !found {
		return entries, false, err
	}
	return removeCatalogEntry(entries, removed.Path), true, nil
}

func upsertCatalogEntry(entries []catalog.Entry, updated catalog.Entry) []catalog.Entry {
	for index := range entries {
		if entries[index].Path == updated.Path {
			entries[index] = updated
			return entries
		}
	}
	return append(entries, updated)
}

func readingOptionsMatch(entry catalog.Entry, options listenOptions) bool {
	return entry.TargetLanguage == options.targetLanguage && entry.CodeMode == options.code
}

func transferCheckpoint(ctx context.Context, cacheRoot, oldPath, newPath string) error {
	oldStore, err := session.NewStore(cacheRoot, oldPath)
	if err != nil {
		return err
	}
	state, found, err := oldStore.Load(ctx)
	if err != nil || !found || state.Complete {
		return err
	}
	newStore, err := session.NewStore(cacheRoot, newPath)
	if err != nil {
		return err
	}
	if _, found, err := newStore.Load(ctx); err != nil {
		return err
	} else if found {
		// A relink destination may already be a known source with its own
		// progress. Preserve that checkpoint rather than replacing it with the
		// moved source's position.
		return nil
	}
	state.SourcePath = ""
	return newStore.Save(ctx, state)
}

func relinkNote(note string, found bool, old catalog.Entry) string {
	if !found {
		return note
	}
	message := "Source relinked."
	if old.Title != "" {
		message = "“" + old.Title + "” relinked."
	}
	if note == "" {
		return message
	}
	return message + " " + note
}

func relinkHomeNote(note string, replacementSaved, found bool, old catalog.Entry) string {
	if !replacementSaved {
		return note
	}
	return relinkNote(note, found, old)
}

func checkpointLabel(cacheRoot, sourcePath string) string {
	store, err := session.NewStore(cacheRoot, sourcePath)
	if err != nil {
		return "Saved place"
	}
	state, found, err := store.Load(context.Background())
	if err != nil || !found {
		return "Saved place"
	}
	if state.Page > 0 {
		return fmt.Sprintf("PDF page %d", state.Page)
	}
	// The first title in a stored path may be the book/document node itself.
	// Without a deeper structural path, the UI cannot safely call that title a
	// chapter, so retain the checkpoint without presenting a false location.
	if len(state.SectionPath) < 2 {
		return "Saved place"
	}
	first := strings.TrimSpace(state.SectionPath[0])
	for index := len(state.SectionPath) - 1; index >= 0; index-- {
		if title := strings.TrimSpace(state.SectionPath[index]); title != "" {
			if index == 0 || strings.EqualFold(title, first) {
				return "Saved place"
			}
			return title
		}
	}
	return "Saved place"
}

func homeCatalogFailure(note string) bool {
	return strings.Contains(note, "could not be added") || strings.Contains(note, "home is unavailable")
}
