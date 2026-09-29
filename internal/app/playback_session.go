package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/pipeline"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/speech"
)

type streamCompletion struct {
	report pipeline.StreamReport
	err    error
}

type playbackDecision struct {
	report      pipeline.StreamReport
	nextChapter int
	stop        bool
	err         error
}

type playerScreen interface {
	Actions() <-chan playerui.Action
	SetStatus(string)
	SetProgress(int, int)
	SetChapter(int)
	SetPaused(bool)
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
			ID: id, Title: label, StartIndex: index, StartSeq: item.Seq,
		})
		previousID = id
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
) (pipeline.StreamReport, bool, error) {
	var display playerScreen
	if deps.interactivePlayback(stdin, stderr) {
		model := playerui.NewPlayer(title, chapters, start.ChapterIndex, len(fullPlan.Segments))
		var err error
		display, err = deps.startPlaybackScreen(stdin, stderr, model)
		if err != nil {
			return pipeline.StreamReport{}, false, apperr.New("player_unavailable", "Could not open the playback controls.", "Run listen from a Linux terminal, or use --no-play to prepare audio without playback.", err)
		}
		if uiRef != nil {
			*uiRef = display
		}
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
	for {
		if currentIndex < 0 || currentIndex >= len(fullPlan.Segments) {
			return pipeline.StreamReport{}, false, errors.New("selected chapter is outside the segment plan")
		}
		currentPlan := segment.Plan{
			DocumentID: fullPlan.DocumentID,
			Segments:   append([]segment.Segment(nil), fullPlan.Segments[currentIndex:]...),
		}
		decision := runPlaybackPass(ctx, stderr, deps, options, fullPlan, currentPlan, currentOffset, currentChapter, chapters,
			synthesizer, audioCache, streamOptions, sessionStore, baseState, display)
		if decision.err != nil {
			return decision.report, false, decision.err
		}
		if decision.stop {
			return decision.report, true, nil
		}
		if decision.nextChapter < 0 {
			return decision.report, false, nil
		}
		currentChapter = decision.nextChapter
		currentIndex = chapters[currentChapter].StartIndex
		currentOffset = 0
		if display != nil {
			display.SetChapter(currentChapter)
			display.SetPaused(false)
			display.SetProgress(0, len(fullPlan.Segments)-currentIndex)
			display.SetStatus("Loading " + chapters[currentChapter].Title)
		}
	}
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
	if sessionStore != nil {
		initial, ok := session.RecordPosition(baseState, fullPlan, currentPlan.Segments[0].Seq, resumeOffset)
		if ok {
			if err := sessionStore.Save(ctx, initial); err != nil {
				return playbackDecision{nextChapter: -1, err: apperr.New("session_save_failed", "Could not save the playback position.", "Check that your cache directory is writable.", err)}
			}
			baseState = initial
		}
		monitor = session.StartMonitor(ctx, player, sessionStore, baseState, fullPlan, deps.sessionPollInterval)
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
		display.SetPaused(false)
		display.SetProgress(0, len(currentPlan.Segments))
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
		report, err := pipeline.Stream(streamCtx, currentPlan, synthesizer, audioCache, player, runOptions)
		streamResults <- streamCompletion{report: report, err: err}
	}()
	streamRunning := true
	var streamReport pipeline.StreamReport
	var waitResults <-chan error
	var ticks <-chan time.Time
	var ticker *time.Ticker
	if display != nil {
		ticker = time.NewTicker(time.Second)
		ticks = ticker.C
		defer ticker.Stop()
	}
	paused := false

	for {
		select {
		case completion := <-streamResults:
			streamRunning = false
			if completion.err != nil {
				_ = stopMonitor()
				if ctx.Err() != nil {
					return playbackDecision{nextChapter: -1, err: ctx.Err()}
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
			return playbackDecision{report: streamReport, nextChapter: -1}
		case action := <-actionChannel(display):
			switch action.Kind {
			case playerui.ActionTogglePause:
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
			case playerui.ActionPrevious, playerui.ActionNext:
				target := currentChapter
				if action.Kind == playerui.ActionPrevious {
					target--
				} else {
					target++
				}
				if target < 0 || target >= len(chapters) || chapters[target].StartIndex >= len(fullPlan.Segments) {
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
					if completion.err != nil && !errors.Is(completion.err, context.Canceled) && ctx.Err() == nil {
						return playbackDecision{nextChapter: -1, err: completion.err}
					}
					streamReport = completion.report
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
				display.SetPosition(sequence, offset, duration)
				chapter := chapterIndexForSequence(chapters, sequence)
				if chapter != currentChapter {
					currentChapter = chapter
					display.SetChapter(chapter)
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
