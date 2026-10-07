package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/pipeline"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/speech"
)

const playerActionTimeout = 750 * time.Millisecond

// Let Bubble Tea dispatch a chapter key that raced with stream completion.
const chapterActionGracePeriod = 50 * time.Millisecond

type streamCompletion struct {
	report pipeline.StreamReport
	err    error
}

type playbackDecision struct {
	report      pipeline.StreamReport
	nextChapter int
	stop        bool
	returnHome  bool
	err         error
}

type PlaybackEnd uint8

const (
	PlaybackCompleted PlaybackEnd = iota
	PlaybackStopped
	PlaybackReturnedHome
)

type liveSegmentProducer func(context.Context, func(segment.Segment) error, func() error, func(string)) error

type playerScreen interface {
	Actions() <-chan playerui.Action
	SetStatus(string)
	SetProgress(int, int)
	SetChapter(int)
	SetPage(int)
	SetPlaybackRate(float64)
	SetPaused(bool)
	SetSeekPending(time.Duration)
	SetPlaybackStarted()
	SetPosition(uint64, time.Duration, time.Duration)
	SetFinished(string)
	Close()
}

func chaptersForPlan(plan segment.Plan, title, sourceName string) []playerui.Chapter {
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(sourceName), filepath.Ext(sourceName))
	}
	if title == "" {
		title = "Full document"
	}
	var chapters []playerui.Chapter
	var previousID string
	for index, item := range plan.Segments {
		id := item.MajorUnitID
		if id == "" {
			id = plan.DocumentID
		}
		if index > 0 && id == previousID {
			continue
		}
		sectionPath := item.SectionPath
		if len(sectionPath) > 0 && strings.EqualFold(strings.TrimSpace(sectionPath[0]), strings.TrimSpace(title)) {
			sectionPath = sectionPath[1:]
		}
		label := strings.Join(sectionPath, "  ›  ")
		if label == "" {
			label = "Opening"
		}
		chapters = append(chapters, playerui.Chapter{
			ID: id, Title: label, StartIndex: index, StartSeq: item.Seq, Page: item.Source.Page,
		})
		previousID = id
	}
	return chapters
}

type chapterOutlineInfo struct {
	node                         *domain.OutlineNode
	hasChapterAncestor           bool
	hasExcludedContainerAncestor bool
}

// markPrimaryChapterStops keeps chapter nodes with no chapter ancestor and no
// front/back-matter or appendix ancestor as the default navigation targets.
// This uses semantic outline ancestry because EPUB parts can place real
// chapters at different absolute depths, while nested sections and appendix
// entries may also be labeled as chapters. Other stops remain searchable.
func markPrimaryChapterStops(chapters []playerui.Chapter, outline []*domain.OutlineNode) []playerui.Chapter {
	if len(chapters) == 0 || len(outline) == 0 {
		return chapters
	}
	infoByID := make(map[string]chapterOutlineInfo)
	var visit func(*domain.OutlineNode, bool, bool) bool
	visit = func(node *domain.OutlineNode, hasChapterAncestor, hasExcludedContainerAncestor bool) bool {
		if node == nil {
			return false
		}
		isChapter := node.Role == domain.RoleChapter
		isExcludedContainer := node.Role == domain.RoleAppendix || node.Role == domain.RoleFrontMatter || node.Role == domain.RoleBackMatter
		hasChapterDescendant := false
		for _, child := range node.Children {
			if visit(child, hasChapterAncestor || isChapter, hasExcludedContainerAncestor || isExcludedContainer) {
				hasChapterDescendant = true
			}
		}
		if node.ID != "" {
			infoByID[node.ID] = chapterOutlineInfo{
				node: node, hasChapterAncestor: hasChapterAncestor,
				hasExcludedContainerAncestor: hasExcludedContainerAncestor,
			}
		}
		return isChapter || hasChapterDescendant
	}
	for _, root := range outline {
		visit(root, false, false)
	}

	matched, primary := 0, 0
	for index := range chapters {
		info, ok := infoByID[chapters[index].ID]
		if !ok {
			continue
		}
		matched++
		chapters[index].Primary = info.node.Role == domain.RoleChapter && !info.hasChapterAncestor && !info.hasExcludedContainerAncestor
		if chapters[index].Primary {
			primary++
		}
	}
	if matched == 0 || primary == 0 {
		for index := range chapters {
			chapters[index].Primary = true
		}
	}
	return chapters
}

func chapterIndexAt(chapters []playerui.Chapter, segmentIndex int) int {
	if len(chapters) == 0 {
		return 0
	}
	selected := 0
	for index, chapter := range chapters {
		if chapter.StartIndex > segmentIndex {
			break
		}
		selected = index
	}
	return selected
}

func chapterIndexForSequence(chapters []playerui.Chapter, sequence uint64) int {
	if len(chapters) == 0 {
		return 0
	}
	index := 0
	for current, chapter := range chapters {
		if chapter.StartSeq > sequence {
			break
		}
		index = current
	}
	return index
}

func pageForPlaybackPosition(plan segment.Plan, recorder session.PositionRecorder, sequence uint64, offset time.Duration) int {
	if recorder != nil {
		if state, ok := recorder(session.State{}, sequence, offset); ok && state.Page > 0 {
			return state.Page
		}
	}
	for _, item := range plan.Segments {
		if item.Seq == sequence {
			return item.Source.Page
		}
	}
	return 0
}

func setDisplayPosition(display playerScreen, plan segment.Plan, recorder session.PositionRecorder, sequence uint64, offset, duration time.Duration) {
	if display == nil {
		return
	}
	display.SetPosition(sequence, offset, duration)
	display.SetPage(pageForPlaybackPosition(plan, recorder, sequence, offset))
}

func docTitle(title, sourceName string) string {
	if title != "" {
		return title
	}
	if base := strings.TrimSuffix(filepath.Base(sourceName), filepath.Ext(sourceName)); base != "" {
		return base
	}
	return "Full document"
}

func runPlayback(
	ctx context.Context,
	stdin io.Reader,
	stderr io.Writer,
	deps runtimeDeps,
	options listenOptions,
	title string,
	fullPlan segment.Plan,
	start playerui.StartPoint,
	chapters []playerui.Chapter,
	synthesizer speech.Synthesizer,
	audioCache pipeline.SpeechCache,
	streamOptions pipeline.StreamOptions,
	sessionStore *session.Store,
	baseState session.State,
	uiRef *playerScreen,
	liveProducer liveSegmentProducer,
	checkpointRecorder session.PositionRecorder,
) (pipeline.StreamReport, PlaybackEnd, error) {
	var display playerScreen
	if uiRef != nil && *uiRef != nil {
		display = *uiRef
	} else if deps.interactivePlayback(stdin, stderr) {
		total := len(fullPlan.Segments)
		status := chapterPreparationStatus(streamOptions.Translator != nil)
		if liveProducer != nil {
			total = 0
			status = "Recognizing PDF pages as you listen"
		}
		model := playerui.NewPlayer(title, chapters, start.ChapterIndex, total)
		model.SetPlaybackRate(options.playbackSpeed)
		if liveProducer != nil {
			model.SetChapterNavigation(false, "PDF recognition continues.")
		}
		model.SetProgress(0, max(0, total-start.StartIndex))
		model.SetStatus(status)
		var err error
		display, err = deps.startPlaybackScreen(stdin, stderr, model)
		if err != nil {
			return pipeline.StreamReport{}, PlaybackCompleted, apperr.New("player_unavailable", "Could not open the playback controls.", "Run listen from a Linux terminal, or use --no-play to prepare audio without playback.", err)
		}
		if uiRef != nil {
			*uiRef = display
		}
	}
	if display != nil {
		defer func() {
			display.Close()
			if uiRef != nil {
				*uiRef = nil
			}
		}()
	}

	currentIndex := start.StartIndex
	currentChapter := start.ChapterIndex
	currentOffset := start.Offset
	currentPage := start.Page
	if currentPage <= 0 && currentChapter >= 0 && currentChapter < len(chapters) {
		currentPage = chapters[currentChapter].Page
	}
	for {
		if currentIndex < 0 || currentIndex >= len(fullPlan.Segments) {
			return pipeline.StreamReport{}, PlaybackCompleted, errors.New("selected chapter is outside the segment plan")
		}
		if display != nil {
			display.SetPage(currentPage)
		}
		currentPlan := segment.Plan{
			DocumentID: fullPlan.DocumentID,
			Segments:   append([]segment.Segment(nil), fullPlan.Segments[currentIndex:]...),
		}
		decision := runPlaybackPass(ctx, stderr, deps, options, fullPlan, currentPlan, currentOffset, currentChapter, chapters,
			synthesizer, audioCache, streamOptions, sessionStore, baseState, display, liveProducer, checkpointRecorder)
		if decision.err != nil {
			return decision.report, PlaybackCompleted, decision.err
		}
		if decision.stop {
			if display != nil {
				display.SetFinished("Playback stopped")
				if exitAfterPlayback, ok := display.(interface{ ExitAfterPlayback() }); ok {
					exitAfterPlayback.ExitAfterPlayback()
				}
			}
			return decision.report, PlaybackStopped, nil
		}
		if decision.returnHome {
			if display != nil {
				display.SetStatus("Saving your place")
			}
			return decision.report, PlaybackReturnedHome, nil
		}
		if decision.nextChapter < 0 {
			return decision.report, PlaybackCompleted, nil
		}
		currentChapter = decision.nextChapter
		currentIndex = chapters[currentChapter].StartIndex
		currentOffset = 0
		if display != nil {
			display.SetChapter(currentChapter)
			currentPage = chapters[currentChapter].Page
			display.SetPage(currentPage)
			display.SetPaused(false)
			display.SetProgress(0, len(fullPlan.Segments)-currentIndex)
			display.SetStatus(chapterPreparationStatus(streamOptions.Translator != nil))
		}
	}
}

func chapterPreparationStatus(translating bool) string {
	if translating {
		return "Translating and preparing audio"
	}
	return "Preparing audio"
}

func runPlaybackPass(
	ctx context.Context,
	stderr io.Writer,
	deps runtimeDeps,
	options listenOptions,
	fullPlan, currentPlan segment.Plan,
	resumeOffset time.Duration,
	currentChapter int,
	chapters []playerui.Chapter,
	synthesizer speech.Synthesizer,
	audioCache pipeline.SpeechCache,
	streamOptions pipeline.StreamOptions,
	sessionStore *session.Store,
	baseState session.State,
	display playerScreen,
	liveProducer liveSegmentProducer,
	checkpointRecorder session.PositionRecorder,
) playbackDecision {
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	playerStderr := stderr
	if display != nil {
		playerStderr = io.Discard
	}
	player := deps.newPlayer(playback.MPVOptions{PlaybackSpeed: options.playbackSpeed, Stderr: playerStderr})
	if player == nil {
		return playbackDecision{nextChapter: -1, err: apperr.New("player_unavailable", "Could not create the audio player.", "Run listen doctor to check mpv, or use --no-play to prepare audio without playback.", nil)}
	}
	defer player.Close()

	var monitor *session.Monitor
	if sessionStore != nil && checkpointRecorder == nil {
		initial, ok := session.RecordPosition(baseState, fullPlan, currentPlan.Segments[0].Seq, resumeOffset)
		if ok {
			if err := sessionStore.Save(ctx, initial); err != nil {
				return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Could not save the playback position.", "Check that your cache directory is writable.", err)}
			}
			baseState = initial
		}
		monitor = session.StartMonitor(ctx, player, sessionStore, baseState, fullPlan, deps.sessionPollInterval)
	} else if sessionStore != nil {
		monitor = session.StartMonitorWithRecorder(ctx, player, sessionStore, baseState, checkpointRecorder, deps.sessionPollInterval)
	}
	stopMonitor := func() error {
		if monitor == nil {
			return nil
		}
		return monitor.Stop()
	}
	defer stopMonitor()

	runOptions := streamOptions
	runOptions.ResumeOffset = resumeOffset
	if display != nil {
		display.SetChapter(currentChapter)
		page := pageForPlaybackPosition(fullPlan, checkpointRecorder, currentPlan.Segments[0].Seq, resumeOffset)
		if page > 0 {
			display.SetPage(page)
		}
		display.SetPaused(false)
		total := len(currentPlan.Segments)
		if liveProducer != nil {
			total = 0
		}
		display.SetProgress(0, total)
	}
	if progress := streamOptions.OnProgress; progress != nil {
		runOptions.OnProgress = func(completed, total int) {
			if display != nil {
				display.SetProgress(completed, total)
				return
			}
			progress(completed, total)
		}
	}
	if display != nil {
		runOptions.OnPlaybackStart = display.SetPlaybackStarted
	}
	streamResults := make(chan streamCompletion, 1)
	go func() {
		var report pipeline.StreamReport
		var err error
		if liveProducer != nil {
			produce := func(produceCtx context.Context, emit func(segment.Segment) error, sourceBoundary func() error) error {
				return liveProducer(produceCtx, emit, sourceBoundary, func(status string) {
					if display != nil {
						display.SetStatus(status)
					} else {
						_, _ = fmt.Fprintln(stderr, status)
					}
				})
			}
			report, err = pipeline.StreamFrom(streamCtx, currentPlan.Segments[0].Seq, produce, synthesizer, audioCache, player, runOptions)
		} else {
			report, err = pipeline.Stream(streamCtx, currentPlan, synthesizer, audioCache, player, runOptions)
		}
		streamResults <- streamCompletion{report: report, err: err}
	}()
	streamRunning := true
	var streamReport pipeline.StreamReport
	var deferredStreamErr error
	var waitResults <-chan error
	var ticks <-chan time.Time
	var ticker *time.Ticker
	if display != nil {
		ticker = time.NewTicker(time.Second)
		ticks = ticker.C
		defer ticker.Stop()
	}
	paused := false
	chapterBrowsing := false
	wasPausedBeforeChapterBrowser := false
	var pendingSeek time.Duration
	resumeAfterSeek := false
	setPendingSeek := func(delta time.Duration) {
		pendingSeek = delta
		if display != nil {
			display.SetSeekPending(delta)
		}
	}
	finishPendingSeek := func(status string) {
		keepPaused := resumeAfterSeek
		setPendingSeek(0)
		resumeAfterSeek = false
		if !keepPaused {
			if pausable, ok := player.(playback.Pausable); ok {
				if err := pausable.SetPaused(streamCtx, false); err != nil {
					paused = true
					if display != nil {
						display.SetPaused(true)
						display.SetStatus("Audio is ready, but playback did not resume")
					}
					return
				}
			}
		}
		paused = keepPaused
		if display != nil {
			display.SetPaused(paused)
			display.SetStatus(status)
		}
	}
	restoreChapterBrowser := func(status string) {
		chapterBrowsing = false
		if !wasPausedBeforeChapterBrowser {
			if pausable, ok := player.(playback.Pausable); ok {
				if err := pausable.SetPaused(streamCtx, false); err != nil {
					paused = true
					if display != nil {
						display.SetPaused(true)
						display.SetStatus("Chapters closed · playback did not resume")
					}
					return
				}
			}
			paused = false
		} else {
			paused = true
		}
		if display != nil {
			display.SetPaused(paused)
			display.SetStatus(status)
		}
	}
	completePendingSeek := func(position playback.Position) {
		if display != nil {
			setDisplayPosition(display, fullPlan, checkpointRecorder, position.Seq, position.Offset, position.Duration)
			chapter := chapterIndexForSequence(chapters, position.Seq)
			if chapter != currentChapter {
				currentChapter = chapter
				display.SetChapter(chapter)
			}
		}
		status := "Playing · seek complete"
		if resumeAfterSeek {
			status = "Paused after seeking"
		}
		finishPendingSeek(status)
	}

	for {
		select {
		case completion := <-streamResults:
			streamRunning = false
			if completion.err != nil {
				_ = stopMonitor()
				if ctx.Err() != nil {
					return playbackDecision{nextChapter: -1, err: ctx.Err()}
				}
				if liveProducer != nil && completion.report.Appended > 0 {
					streamReport = completion.report
					deferredStreamErr = completion.err
					status := "Page recognition stopped · finishing audio that is already ready"
					if display != nil {
						display.SetStatus(status)
					} else {
						_, _ = fmt.Fprintln(stderr, status)
					}
					waitResult := make(chan error, 1)
					waitResults = waitResult
					go func() { waitResult <- player.Wait(streamCtx) }()
					continue
				}
				if target, ok := pendingChapterNavigation(actionChannel(display), currentChapter, chapters, len(fullPlan.Segments), chapterActionGracePeriod); ok {
					cancelStream()
					return playbackDecision{report: completion.report, nextChapter: target}
				}
				return playbackDecision{nextChapter: -1, err: completion.err}
			}
			streamReport = completion.report
			waitResult := make(chan error, 1)
			waitResults = waitResult
			go func() { waitResult <- player.Wait(streamCtx) }()
		case err := <-waitResults:
			if saveErr := stopMonitor(); saveErr != nil {
				return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Playback finished, but its last position could not be saved.", "Completed audio remains in the local cache.", saveErr)}
			}
			if err != nil {
				if ctx.Err() != nil {
					return playbackDecision{nextChapter: -1, err: ctx.Err()}
				}
				return playbackDecision{nextChapter: -1, err: err}
			}
			if deferredStreamErr != nil {
				return playbackDecision{report: streamReport, nextChapter: -1, err: deferredStreamErr}
			}
			return playbackDecision{report: streamReport, nextChapter: -1}
		case action := <-actionChannel(display):
			switch action.Kind {
			case playerui.ActionPlaybackRate:
				rateSetter, ok := player.(playback.RateSetter)
				if !ok {
					display.SetStatus("Playback speed cannot change with this audio player")
					continue
				}
				if err := rateSetter.SetPlaybackRate(streamCtx, action.Rate); err != nil {
					display.SetStatus("Could not change playback speed")
					continue
				}
				display.SetPlaybackRate(action.Rate)
				saved := false
				userPaths, pathErr := paths.FromEnv()
				if pathErr == nil {
					config, _, loadErr := settings.Load(userPaths.ConfigDir)
					if loadErr == nil {
						config.PlaybackRate = action.Rate
						loadErr = settings.Save(context.WithoutCancel(ctx), userPaths.ConfigDir, config)
						saved = loadErr == nil
					}
				}
				if saved {
					display.SetStatus(fmt.Sprintf("Playback speed %.1fx · saved for next session", action.Rate))
				} else {
					display.SetStatus(fmt.Sprintf("Playback speed %.1fx · could not save preference", action.Rate))
				}
			case playerui.ActionOpenChapters:
				wasPausedBeforeChapterBrowser = paused
				chapterBrowsing = true
				if !paused {
					pausable, ok := player.(playback.Pausable)
					if !ok {
						if screen, ok := display.(interface{ CancelChapterBrowser() }); ok {
							screen.CancelChapterBrowser()
						}
						chapterBrowsing = false
						display.SetStatus("Pausing is unavailable · close chapters with Esc")
						continue
					}
					if err := pausable.SetPaused(streamCtx, true); err != nil {
						if screen, ok := display.(interface{ CancelChapterBrowser() }); ok {
							screen.CancelChapterBrowser()
						}
						chapterBrowsing = false
						display.SetStatus("Could not pause · close chapters with Esc")
						continue
					}
					paused = true
				}
				if display != nil {
					display.SetPaused(true)
					display.SetStatus("Chapters · playback paused")
				}
			case playerui.ActionCancelChapters:
				if chapterBrowsing {
					status := "Playing"
					if wasPausedBeforeChapterBrowser {
						status = "Paused"
					}
					restoreChapterBrowser(status)
				}
			case playerui.ActionSelectChapter:
				target := action.Start.ChapterIndex
				if !chapterBrowsing || target < 0 || target >= len(chapters) || chapters[target].StartIndex >= len(fullPlan.Segments) {
					if chapterBrowsing {
						restoreChapterBrowser("That chapter is unavailable")
					}
					continue
				}
				if liveProducer != nil {
					restoreChapterBrowser("PDF pages are still being recognized")
					continue
				}
				if err := stopMonitor(); err != nil {
					return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Could not save the playback position before changing chapters.", "Check that your cache directory is writable.", err)}
				}
				cancelStream()
				if streamRunning {
					completion := <-streamResults
					streamRunning = false
					if ctx.Err() != nil {
						return playbackDecision{nextChapter: -1, err: ctx.Err()}
					}
					if completion.err == nil {
						streamReport = completion.report
					}
				}
				return playbackDecision{report: streamReport, nextChapter: target}
			case playerui.ActionTogglePause:
				if pendingSeek != 0 {
					setPendingSeek(0)
					resumeAfterSeek = false
				}
				pausable, ok := player.(playback.Pausable)
				if !ok {
					display.SetStatus("Pause control is unavailable for this player")
					continue
				}
				paused = !paused
				if err := pausable.SetPaused(streamCtx, paused); err != nil {
					paused = !paused
					display.SetStatus("Could not change playback state")
					continue
				}
				display.SetPaused(paused)
				display.SetStatus(map[bool]string{true: "Paused", false: "Playing"}[paused])
			case playerui.ActionSeekBackward, playerui.ActionSeekForward:
				delta := -5 * time.Second
				if action.Kind == playerui.ActionSeekForward {
					delta = 5 * time.Second
				}
				seeker, ok := player.(playback.BufferedSeeker)
				if !ok {
					display.SetStatus("Seeking is unavailable for this player")
					continue
				}
				if pendingSeek != 0 {
					pendingSeek += delta
					if pendingSeek == 0 {
						status := "Playing"
						if resumeAfterSeek {
							status = "Paused"
						}
						finishPendingSeek(status)
						continue
					}
					setPendingSeek(pendingSeek)
					continue
				}
				position, err := seekByWithTimeout(streamCtx, seeker, delta)
				if errors.Is(err, playback.ErrSeekNotBuffered) {
					if !streamRunning {
						display.SetStatus("End of ready audio · cannot seek farther")
						continue
					}
					pausable, canPause := player.(playback.Pausable)
					if !canPause {
						display.SetStatus("More audio is needed before seeking")
						continue
					}
					resumeAfterSeek = paused
					if !paused {
						if pauseErr := pausable.SetPaused(streamCtx, true); pauseErr != nil {
							display.SetStatus("Could not pause while audio buffers")
							continue
						}
						paused = true
						display.SetPaused(true)
					}
					setPendingSeek(delta)
					continue
				}
				if err != nil {
					display.SetStatus(seekFailureStatus(err))
					continue
				}
				setDisplayPosition(display, fullPlan, checkpointRecorder, position.Seq, position.Offset, position.Duration)
				chapter := chapterIndexForSequence(chapters, position.Seq)
				if chapter != currentChapter {
					currentChapter = chapter
					display.SetChapter(chapter)
				}
				if delta > 0 {
					display.SetStatus("Skipped ahead 5 seconds")
				} else {
					display.SetStatus("Skipped back 5 seconds")
				}
			case playerui.ActionPrevious, playerui.ActionNext:
				if liveProducer != nil {
					display.SetStatus("PDF chapter navigation is unavailable while pages are being recognized")
					continue
				}
				target, valid := chapterTargetForAction(action, currentChapter, chapters, len(fullPlan.Segments))
				if !valid {
					if target < 0 {
						display.SetStatus("You are at the first chapter")
					} else {
						display.SetStatus("You are at the last chapter")
					}
					continue
				}
				if err := stopMonitor(); err != nil {
					return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Could not save the playback position before changing chapters.", "Check that your cache directory is writable.", err)}
				}
				cancelStream()
				if streamRunning {
					completion := <-streamResults
					streamRunning = false
					if ctx.Err() != nil {
						return playbackDecision{nextChapter: -1, err: ctx.Err()}
					}
					// The stream belongs to the chapter being replaced. Once the
					// user requests navigation, teardown errors from that superseded
					// stream must not be reported as a source failure.
					if completion.err == nil {
						streamReport = completion.report
					}
				}
				return playbackDecision{report: streamReport, nextChapter: target}
			case playerui.ActionStop:
				saveErr := stopMonitor()
				cancelStream()
				if streamRunning {
					<-streamResults
				}
				if saveErr != nil {
					return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Could not save the playback position.", "Check that your cache directory is writable.", saveErr)}
				}
				return playbackDecision{report: streamReport, nextChapter: -1, stop: true}
			case playerui.ActionReturnHome:
				if display != nil {
					display.SetStatus("Saving your place")
				}
				saveErr := stopMonitor()
				cancelStream()
				if streamRunning {
					<-streamResults
				}
				if saveErr != nil {
					return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Could not save the playback position.", "Check that your cache directory is writable.", saveErr)}
				}
				return playbackDecision{report: streamReport, nextChapter: -1, returnHome: true}
			}
		case <-ticks:
			queryCtx, cancelQuery := context.WithTimeout(ctx, 750*time.Millisecond)
			sequence, offset, positionErr := player.Position(queryCtx)
			var duration time.Duration
			if reader, ok := player.(playback.DurationReader); ok {
				duration, _ = reader.CurrentDuration(queryCtx)
			}
			cancelQuery()
			if positionErr == nil {
				setDisplayPosition(display, fullPlan, checkpointRecorder, sequence, offset, duration)
				chapter := chapterIndexForSequence(chapters, sequence)
				if chapter != currentChapter {
					currentChapter = chapter
					display.SetChapter(chapter)
				}
			}
			if pendingSeek != 0 {
				seeker, ok := player.(playback.BufferedSeeker)
				if !ok {
					finishPendingSeek("Seeking is unavailable for this player")
					continue
				}
				position, seekErr := seekByWithTimeout(streamCtx, seeker, pendingSeek)
				if seekErr == nil {
					completePendingSeek(position)
					continue
				}
				if !errors.Is(seekErr, playback.ErrSeekNotBuffered) {
					finishPendingSeek(seekFailureStatus(seekErr))
					continue
				}
				if !streamRunning {
					// Generation is complete, so no more playable audio will be
					// appended. Keep the current position and explain the limit.
					finishPendingSeek("End of ready audio · seek cancelled")
					continue
				}
			}
		case <-ctx.Done():
			if err := stopMonitor(); err != nil {
				return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Could not save the playback position while stopping.", "Check that your cache directory is writable.", err)}
			}
			cancelStream()
			if streamRunning {
				<-streamResults
			}
			return playbackDecision{nextChapter: -1, err: ctx.Err()}
		}
	}
}

func actionChannel(display playerScreen) <-chan playerui.Action {
	if display == nil {
		return nil
	}
	return display.Actions()
}

func chapterTargetForAction(action playerui.Action, currentChapter int, chapters []playerui.Chapter, segmentCount int) (int, bool) {
	stops := make([]int, 0, len(chapters))
	for index, chapter := range chapters {
		if chapter.Primary {
			stops = append(stops, index)
		}
	}
	if len(stops) == 0 {
		for index := range chapters {
			stops = append(stops, index)
		}
	}
	currentStop := -1
	if currentChapter >= 0 && currentChapter < len(chapters) {
		currentStart := chapters[currentChapter].StartIndex
		for position, index := range stops {
			if chapters[index].StartIndex > currentStart {
				break
			}
			currentStop = position
		}
	}
	targetStop := currentStop
	switch action.Kind {
	case playerui.ActionPrevious:
		targetStop--
	case playerui.ActionNext:
		targetStop++
	default:
		return 0, false
	}
	if targetStop < 0 {
		return -1, false
	}
	if targetStop >= len(stops) {
		return len(chapters), false
	}
	target := stops[targetStop]
	if chapters[target].StartIndex >= segmentCount {
		return target, false
	}
	return target, true
}

func pendingChapterNavigation(actions <-chan playerui.Action, currentChapter int, chapters []playerui.Chapter, segmentCount int, grace time.Duration) (int, bool) {
	if actions == nil {
		return 0, false
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	for {
		select {
		case action := <-actions:
			if target, ok := chapterTargetForAction(action, currentChapter, chapters, segmentCount); ok {
				return target, true
			}
		case <-timer.C:
			return 0, false
		}
	}
}

func seekByWithTimeout(ctx context.Context, seeker playback.BufferedSeeker, delta time.Duration) (playback.Position, error) {
	seekCtx, cancel := context.WithTimeout(ctx, playerActionTimeout)
	defer cancel()
	return seeker.SeekBy(seekCtx, delta)
}

func seekFailureStatus(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Seek timed out · try again"
	}
	return "Could not seek audio"
}

func playerError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return interrupted(err)
	}
	var appError *apperr.Error
	if errors.As(err, &appError) {
		return err
	}
	return apperr.New("playback_failed", "Translation, audio generation, or playback stopped before the source finished.", "Completed work is safe; run listen again to resume.", err)
}
