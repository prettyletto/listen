package app

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/paths"
	"github.com/prettyletto/listen/internal/pipeline"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/testkit"
)

func TestChaptersForPlanGroupsSegmentsByMajorUnit(t *testing.T) {
	plan := segment.Plan{DocumentID: "book", Segments: []segment.Segment{
		{Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Example Book", "Part I", "Chapter 1"}},
		{Seq: 1, MajorUnitID: "chapter-1", SectionPath: []string{"Example Book", "Part I", "Chapter 1"}},
		{Seq: 2, MajorUnitID: "chapter-2", SectionPath: []string{"Example Book", "Part I", "Chapter 2"}},
	}}
	chapters := chaptersForPlan(plan, "Example Book", "book.md")
	if len(chapters) != 2 {
		t.Fatalf("chapter count = %d, want 2: %#v", len(chapters), chapters)
	}
	if chapters[0].StartIndex != 0 || chapters[0].Title != "Part I  ›  Chapter 1" || chapters[1].Title != "Part I  ›  Chapter 2" || chapters[1].StartIndex != 2 || chapters[1].StartSeq != 2 {
		t.Fatalf("chapter starts = %#v", chapters)
	}
	if strings.Contains(chapters[0].Title, "Example Book") {
		t.Fatalf("chapter title repeats the book title: %q", chapters[0].Title)
	}
	if got := chapterIndexAt(chapters, 2); got != 1 {
		t.Fatalf("resume point mapped to chapter %d, want 1", got)
	}
	if got := chapterIndexForSequence(chapters, 2); got != 1 {
		t.Fatalf("playback sequence mapped to chapter %d, want 1", got)
	}
}

func TestPrimaryChapterStopsIncludeChaptersAtVaryingPartDepthsAndKeepSections(t *testing.T) {
	plan := segment.Plan{DocumentID: "book", Segments: []segment.Segment{
		{Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Example Book", "Chapter 1"}},
		{Seq: 1, MajorUnitID: "section-1-1", SectionPath: []string{"Example Book", "Chapter 1", "Section 1.1"}},
		{Seq: 2, MajorUnitID: "chapter-2", SectionPath: []string{"Example Book", "Part I", "Chapter 2"}},
		{Seq: 3, MajorUnitID: "section-2-1", SectionPath: []string{"Example Book", "Part I", "Chapter 2", "Section 2.1"}},
		{Seq: 4, MajorUnitID: "chapter-3", SectionPath: []string{"Example Book", "Part I", "Chapter 3"}},
		{Seq: 5, MajorUnitID: "chapter-4", SectionPath: []string{"Example Book", "Part II", "Chapter 4"}},
		{Seq: 6, MajorUnitID: "chapter-5", SectionPath: []string{"Example Book", "Part III", "Chapter 5"}},
		{Seq: 7, MajorUnitID: "question-1", SectionPath: []string{"Example Book", "Discussion Questions", "Chapter 1"}},
		{Seq: 8, MajorUnitID: "question-2", SectionPath: []string{"Example Book", "Discussion Questions", "Chapter 2"}},
		{Seq: 9, MajorUnitID: "question-3", SectionPath: []string{"Example Book", "Discussion Questions", "Chapter 3"}},
	}}
	outline := []*domain.OutlineNode{{ID: "book-root", Role: domain.RoleBook, Depth: 0, StartBlock: 0, EndBlockExclusive: 10, Children: []*domain.OutlineNode{
		{ID: "chapter-1", Title: "Chapter 1", Role: domain.RoleChapter, Depth: 1, StartBlock: 0, EndBlockExclusive: 2, Children: []*domain.OutlineNode{
			{ID: "section-1-1", Title: "Section 1.1", Role: domain.RoleChapter, Depth: 2, StartBlock: 1, EndBlockExclusive: 2},
		}},
		{ID: "part-1", Title: "Part I", Role: domain.RolePart, Depth: 1, StartBlock: 2, EndBlockExclusive: 5, Children: []*domain.OutlineNode{
			{ID: "chapter-2", Title: "Chapter 2", Role: domain.RoleChapter, Depth: 2, StartBlock: 2, EndBlockExclusive: 4, Children: []*domain.OutlineNode{
				{ID: "section-2-1", Title: "Section 2.1", Role: domain.RoleChapter, Depth: 3, StartBlock: 3, EndBlockExclusive: 4},
			}},
			{ID: "chapter-3", Title: "Chapter 3", Role: domain.RoleChapter, Depth: 2, StartBlock: 4, EndBlockExclusive: 5},
		}},
		{ID: "part-2", Title: "Part II", Role: domain.RolePart, Depth: 1, StartBlock: 5, EndBlockExclusive: 6, Children: []*domain.OutlineNode{
			{ID: "chapter-4", Title: "Chapter 4", Role: domain.RoleChapter, Depth: 2, StartBlock: 5, EndBlockExclusive: 6},
		}},
		{ID: "part-3", Title: "Part III", Role: domain.RolePart, Depth: 1, StartBlock: 6, EndBlockExclusive: 7, Children: []*domain.OutlineNode{
			{ID: "chapter-5", Title: "Chapter 5", Role: domain.RoleChapter, Depth: 2, StartBlock: 6, EndBlockExclusive: 7},
		}},
		{ID: "discussion-questions", Title: "Discussion Questions", Role: domain.RoleAppendix, Depth: 1, StartBlock: 7, EndBlockExclusive: 10, Children: []*domain.OutlineNode{
			{ID: "question-1", Title: "Chapter 1", Role: domain.RoleChapter, Depth: 2, StartBlock: 7, EndBlockExclusive: 8},
			{ID: "question-2", Title: "Chapter 2", Role: domain.RoleChapter, Depth: 2, StartBlock: 8, EndBlockExclusive: 9},
			{ID: "question-3", Title: "Chapter 3", Role: domain.RoleChapter, Depth: 2, StartBlock: 9, EndBlockExclusive: 10},
		}},
	}}}
	chapters := markPrimaryChapterStops(chaptersForPlan(plan, "Example Book", "book.epub"), outline)
	if len(chapters) != 10 {
		t.Fatalf("full chapter map has %d entries, want 10: %#v", len(chapters), chapters)
	}
	var primaryIDs []string
	for _, chapter := range chapters {
		if chapter.Primary {
			primaryIDs = append(primaryIDs, chapter.ID)
		}
	}
	if strings.Join(primaryIDs, ",") != "chapter-1,chapter-2,chapter-3,chapter-4,chapter-5" {
		t.Fatalf("primary stops = %v, want root chapter plus chapters from Parts I–III", primaryIDs)
	}
	if chapters[3].Title != "Part I  ›  Chapter 2  ›  Section 2.1" || chapters[3].StartIndex != 3 || chapters[3].StartSeq != 3 {
		t.Fatalf("nested section lost its path or playback boundary: %#v", chapters[3])
	}
	for index := 7; index < 10; index++ {
		if chapters[index].Primary {
			t.Errorf("appendix question %q was promoted to a main chapter", chapters[index].Title)
		}
	}
}

func TestPreviousAndNextChapterActionsSkipNestedStops(t *testing.T) {
	chapters := []playerui.Chapter{
		{ID: "chapter-1", StartIndex: 0, Primary: true},
		{ID: "section-1-1", StartIndex: 2},
		{ID: "section-1-2", StartIndex: 4},
		{ID: "chapter-2", StartIndex: 6, Primary: true},
		{ID: "chapter-3", StartIndex: 9, Primary: true},
	}
	if target, ok := chapterTargetForAction(playerui.Action{Kind: playerui.ActionNext}, 2, chapters, 12); !ok || target != 3 {
		t.Fatalf("next from nested section = (%d, %v), want chapter 2 at index 3", target, ok)
	}
	if target, ok := chapterTargetForAction(playerui.Action{Kind: playerui.ActionPrevious}, 3, chapters, 12); !ok || target != 0 {
		t.Fatalf("previous from chapter 2 = (%d, %v), want chapter 1 at index 0", target, ok)
	}
	if target, ok := chapterTargetForAction(playerui.Action{Kind: playerui.ActionNext}, 4, chapters, 12); ok || target != len(chapters) {
		t.Fatalf("next from final primary stop = (%d, %v), want end-of-book", target, ok)
	}
}

func TestPendingChapterNavigationWinsOverJustCompletedStreamError(t *testing.T) {
	chapters := []playerui.Chapter{{Title: "One", StartIndex: 0}, {Title: "Two", StartIndex: 2}}
	actions := make(chan playerui.Action, 2)
	actions <- playerui.Action{Kind: playerui.ActionTogglePause}
	actions <- playerui.Action{Kind: playerui.ActionNext}
	target, ok := pendingChapterNavigation(actions, 0, chapters, 4, 20*time.Millisecond)
	if !ok || target != 1 {
		t.Fatalf("pending chapter target = %d, active=%v; want chapter 1", target, ok)
	}
}

func TestPlaybackPreparationStatusExplainsTranslationWork(t *testing.T) {
	if got := chapterPreparationStatus(false); got != "Preparing audio" {
		t.Fatalf("local preparation status = %q", got)
	}
	if got := chapterPreparationStatus(true); got != "Translating and preparing audio" {
		t.Fatalf("translated preparation status = %q", got)
	}
}

func TestStreamedPDFPlayerDeclaresChapterControlsUnavailable(t *testing.T) {
	plan := segment.Plan{DocumentID: "scanned-pdf", Segments: []segment.Segment{{ID: "placeholder", Seq: 0}}}
	chapters := []playerui.Chapter{{ID: "pdf", Title: "Reading order", StartIndex: 0, StartSeq: 0}}
	var enabled bool
	var reason string
	deps := runtimeDeps{
		interactivePlayback: func(io.Reader, io.Writer) bool { return true },
		startPlaybackScreen: func(_ io.Reader, _ io.Writer, model playerui.Model) (playerScreen, error) {
			enabled, reason = model.ChapterNavigation()
			return nil, errors.New("test stops after observing the model")
		},
	}
	liveProducer := func(context.Context, func(segment.Segment) error, func() error, func(string)) error {
		return errors.New("unexpectedly started PDF production")
	}
	_, _, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
		listenOptions{playbackSpeed: 1}, "Scanned book", plan,
		playerui.StartPoint{StartIndex: 0, StartSeq: 0, ChapterIndex: 0}, chapters,
		&testkit.FakeSynthesizer{Duration: time.Second}, nil,
		pipeline.StreamOptions{StartupBuffer: time.Nanosecond}, nil, session.State{}, nil, liveProducer, nil)
	if err == nil {
		t.Fatal("playback continued after the test screen rejected startup")
	}
	if enabled || !strings.Contains(reason, "PDF recognition") {
		t.Fatalf("streamed-PDF chapter navigation = enabled:%v reason:%q", enabled, reason)
	}
}

func TestPlaybackNextChapterCancelsCurrentStreamAndRestartsAtChapterBoundary(t *testing.T) {
	plan := segment.Plan{DocumentID: "book", Segments: []segment.Segment{
		{ID: "one-a", Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Chapter 1"}},
		{ID: "one-b", Seq: 1, MajorUnitID: "chapter-1", SectionPath: []string{"Chapter 1"}},
		{ID: "two-a", Seq: 2, MajorUnitID: "chapter-2", SectionPath: []string{"Chapter 2"}},
		{ID: "two-b", Seq: 3, MajorUnitID: "chapter-2", SectionPath: []string{"Chapter 2"}},
	}}
	chapters := chaptersForPlan(plan, "Example Book", "book.md")
	screen := &fakePlayerScreen{actions: make(chan playerui.Action, 2), started: make(chan struct{}, 2)}
	players := make(chan *navigationTestPlayer, 4)
	appended := make(chan appendedSegment, 8)
	var count int
	deps := runtimeDeps{
		interactivePlayback: func(io.Reader, io.Writer) bool { return true },
		startPlaybackScreen: func(io.Reader, io.Writer, playerui.Model) (playerScreen, error) { return screen, nil },
		newPlayer: func(playback.MPVOptions) playback.Player {
			count++
			player := &navigationTestPlayer{id: count, appended: appended}
			if count == 1 {
				player.blockAppend = true
				player.blockSequence = 1
				player.blockStarted = make(chan struct{})
			}
			players <- player
			return player
		},
	}
	goSynth := &testkit.FakeSynthesizer{Duration: time.Second}
	result := make(chan struct {
		report  pipeline.StreamReport
		stopped bool
		err     error
	}, 1)
	go func() {
		report, end, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
			listenOptions{playbackSpeed: 1}, "Example Book", plan,
			playerui.StartPoint{StartIndex: 0, StartSeq: 0, ChapterIndex: 0}, chapters,
			goSynth, nil, pipeline.StreamOptions{StartupBuffer: time.Nanosecond}, nil, session.State{}, nil, nil, nil)
		result <- struct {
			report  pipeline.StreamReport
			stopped bool
			err     error
		}{report: report, stopped: end != PlaybackCompleted, err: err}
	}()
	select {
	case <-screen.started:
	case <-time.After(time.Second):
		t.Fatal("first chapter did not start")
	}
	first := <-players
	select {
	case item := <-appended:
		if item.player != 1 || item.seq != 0 {
			t.Fatalf("first chapter queued player=%d sequence=%d", item.player, item.seq)
		}
	case <-time.After(time.Second):
		t.Fatal("first chapter did not enqueue its first segment")
	}
	select {
	case <-first.blockStarted:
	case <-time.After(time.Second):
		t.Fatal("cancellable player append did not start before chapter navigation")
	}
	screen.actions <- playerui.Action{Kind: playerui.ActionNext}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case item := <-appended:
			if item.player == 2 {
				if item.seq != 2 {
					t.Fatalf("next chapter queued sequence %d, want chapter boundary sequence 2", item.seq)
				}
				goto secondChapterQueued
			}
		case <-deadline:
			t.Fatal("next chapter did not start at its first segment")
		}
	}
secondChapterQueued:
	select {
	case <-screen.started:
	case <-time.After(time.Second):
		t.Fatal("second chapter did not start playback")
	}
	second := <-players
	if second.id != 2 {
		t.Fatalf("second player id = %d, want 2", second.id)
	}
	screen.actions <- playerui.Action{Kind: playerui.ActionStop}
	select {
	case got := <-result:
		if got.err != nil || !got.stopped {
			t.Fatalf("stop outcome = stopped:%v err:%v", got.stopped, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopping the interactive player did not cancel the stream")
	}
	if first.closeCalls() == 0 || second.closeCalls() == 0 {
		t.Fatalf("chapter players were not closed: first=%d second=%d", first.closeCalls(), second.closeCalls())
	}
}

func TestPlaybackWaitsForReadyAudioBeforeSeekingAndResumes(t *testing.T) {
	plan := segment.Plan{DocumentID: "book", Segments: []segment.Segment{
		{ID: "one", Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Chapter 1"}},
		{ID: "two", Seq: 1, MajorUnitID: "chapter-1", SectionPath: []string{"Chapter 1"}},
		{ID: "three", Seq: 2, MajorUnitID: "chapter-1", SectionPath: []string{"Chapter 1"}},
	}}
	chapters := chaptersForPlan(plan, "Example Book", "book.md")
	screen := &fakePlayerScreen{
		actions: make(chan playerui.Action, 2), started: make(chan struct{}, 1),
		seekPending: make(chan time.Duration, 4), pauseStates: make(chan bool, 4),
		positions: make(chan playback.Position, 8),
	}
	playerReady := make(chan *bufferedSeekTestPlayer, 1)
	deps := runtimeDeps{
		interactivePlayback: func(io.Reader, io.Writer) bool { return true },
		startPlaybackScreen: func(io.Reader, io.Writer, playerui.Model) (playerScreen, error) { return screen, nil },
		newPlayer: func(playback.MPVOptions) playback.Player {
			player := &bufferedSeekTestPlayer{
				appended: make(chan playback.Item, 4), seekDeadlines: make(chan time.Time, 4),
			}
			playerReady <- player
			return player
		},
	}
	synth := &testkit.FakeSynthesizer{
		Duration:   3 * time.Second,
		DelayBySeq: map[uint64]time.Duration{1: 1200 * time.Millisecond, 2: 10 * time.Second},
	}
	result := make(chan struct {
		stopped bool
		err     error
	}, 1)
	go func() {
		_, end, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
			listenOptions{playbackSpeed: 1}, "Example Book", plan,
			playerui.StartPoint{StartIndex: 0, StartSeq: 0, ChapterIndex: 0}, chapters,
			synth, nil, pipeline.StreamOptions{Workers: 1, StartupBuffer: time.Nanosecond}, nil, session.State{}, nil, nil, nil)
		result <- struct {
			stopped bool
			err     error
		}{stopped: end != PlaybackCompleted, err: err}
	}()

	awaitSignal(t, screen.started, "playback start")
	player := <-playerReady
	awaitItem(t, player.appended, 0)
	screen.actions <- playerui.Action{Kind: playerui.ActionSeekForward}
	select {
	case deadline := <-player.seekDeadlines:
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > playerActionTimeout {
			t.Fatalf("direct seek timeout remaining = %s, want at most %s", remaining, playerActionTimeout)
		}
	case <-time.After(time.Second):
		t.Fatal("seek did not use a bounded player operation context")
	}
	awaitSignalValue(t, screen.pauseStates, true, "player pauses while the seek target is generated")
	awaitSignalValue(t, screen.seekPending, 5*time.Second, "five-second seek buffering feedback")
	awaitPosition(t, screen.positions, playback.Position{Seq: 1, Offset: 2 * time.Second, Duration: 3 * time.Second})
	awaitSignalValue(t, screen.pauseStates, false, "player resumes after the target becomes ready")
	if paused := player.isPaused(); paused {
		t.Fatal("player remained paused after its buffered seek completed")
	}

	screen.actions <- playerui.Action{Kind: playerui.ActionStop}
	select {
	case got := <-result:
		if got.err != nil || !got.stopped {
			t.Fatalf("stop outcome = stopped:%v err:%v", got.stopped, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stopping after buffered seek did not cancel playback")
	}
}

func TestChapterBrowserRestoresPlaybackPauseStateOnCancel(t *testing.T) {
	plan := segment.Plan{DocumentID: "book", Segments: []segment.Segment{
		{ID: "one", Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Chapter 1"}},
		{ID: "two", Seq: 1, MajorUnitID: "chapter-2", SectionPath: []string{"Chapter 2"}},
	}}
	chapters := chaptersForPlan(plan, "Book", "book.txt")
	for _, initiallyPaused := range []bool{false, true} {
		t.Run(map[bool]string{false: "playing", true: "paused"}[initiallyPaused], func(t *testing.T) {
			screen := &fakePlayerScreen{
				actions: make(chan playerui.Action, 8), started: make(chan struct{}, 1),
				pauseStates: make(chan bool, 8),
			}
			playerReady := make(chan *bufferedSeekTestPlayer, 1)
			deps := runtimeDeps{
				interactivePlayback: func(io.Reader, io.Writer) bool { return true },
				startPlaybackScreen: func(io.Reader, io.Writer, playerui.Model) (playerScreen, error) { return screen, nil },
				newPlayer: func(playback.MPVOptions) playback.Player {
					player := &bufferedSeekTestPlayer{appended: make(chan playback.Item, 4)}
					playerReady <- player
					return player
				},
			}
			result := make(chan error, 1)
			go func() {
				_, _, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
					listenOptions{playbackSpeed: 1}, "Book", plan,
					playerui.StartPoint{StartIndex: 0, StartSeq: 0, ChapterIndex: 0}, chapters,
					&testkit.FakeSynthesizer{Duration: time.Second}, nil,
					pipeline.StreamOptions{StartupBuffer: time.Nanosecond}, nil, session.State{}, nil, nil, nil)
				result <- err
			}()
			awaitSignal(t, screen.started, "player start")
			player := <-playerReady
			awaitSignalValue(t, screen.pauseStates, false, "new playback starts unpaused")
			if initiallyPaused {
				screen.actions <- playerui.Action{Kind: playerui.ActionTogglePause}
				awaitSignalValue(t, screen.pauseStates, true, "playback pauses before opening chapters")
			}
			screen.actions <- playerui.Action{Kind: playerui.ActionOpenChapters}
			awaitSignalValue(t, screen.pauseStates, true, "opening chapters pauses playback")
			screen.actions <- playerui.Action{Kind: playerui.ActionCancelChapters}
			awaitSignalValue(t, screen.pauseStates, initiallyPaused, "closing chapter browser restores the prior pause state")
			if player.isPaused() != initiallyPaused {
				t.Fatalf("initially paused=%v, player pause state after cancel=%v", initiallyPaused, player.isPaused())
			}
			screen.actions <- playerui.Action{Kind: playerui.ActionStop}
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("stop after chapter browsing: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("playback did not stop after chapter browsing")
			}
		})
	}
}

func TestChapterBrowserSelectionRestartsAtChosenBoundary(t *testing.T) {
	plan := segment.Plan{DocumentID: "book", Segments: []segment.Segment{
		{ID: "one", Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Chapter 1"}},
		{ID: "two", Seq: 1, MajorUnitID: "chapter-2", SectionPath: []string{"Chapter 2"}},
	}}
	chapters := chaptersForPlan(plan, "Book", "book.txt")
	screen := &fakePlayerScreen{actions: make(chan playerui.Action, 4), started: make(chan struct{}, 2), pauseStates: make(chan bool, 8)}
	players := make(chan *bufferedSeekTestPlayer, 2)
	deps := runtimeDeps{
		interactivePlayback: func(io.Reader, io.Writer) bool { return true },
		startPlaybackScreen: func(io.Reader, io.Writer, playerui.Model) (playerScreen, error) { return screen, nil },
		newPlayer: func(playback.MPVOptions) playback.Player {
			player := &bufferedSeekTestPlayer{appended: make(chan playback.Item, 4)}
			players <- player
			return player
		},
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
			listenOptions{playbackSpeed: 1}, "Book", plan,
			playerui.StartPoint{StartIndex: 0, StartSeq: 0, ChapterIndex: 0}, chapters,
			&testkit.FakeSynthesizer{Duration: time.Second}, nil,
			pipeline.StreamOptions{StartupBuffer: time.Nanosecond}, nil, session.State{}, nil, nil, nil)
		result <- err
	}()
	awaitSignal(t, screen.started, "first chapter playback")
	first := <-players
	awaitItem(t, first.appended, 0)
	screen.actions <- playerui.Action{Kind: playerui.ActionOpenChapters}
	awaitSignalValue(t, screen.pauseStates, true, "chapter picker pauses first chapter")
	screen.actions <- playerui.Action{Kind: playerui.ActionSelectChapter, Start: playerui.StartPoint{ChapterIndex: 1, StartIndex: 1, StartSeq: 1}}
	awaitSignal(t, screen.started, "selected chapter playback")
	second := <-players
	awaitItem(t, second.appended, 1)
	screen.actions <- playerui.Action{Kind: playerui.ActionStop}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("stop after selected chapter: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("playback did not stop after selected chapter")
	}
}

func TestPageForPlaybackPositionUsesStreamResolverForScannedPDF(t *testing.T) {
	plan := segment.Plan{Segments: []segment.Segment{{ID: "placeholder", Seq: 7, Source: domain.SourceRef{Page: 1}}}}
	resolver := session.NewPositionResolver()
	resolver.Add(segment.Segment{ID: "real-passage", Seq: 7, Source: domain.SourceRef{Page: 42}}, 2)
	page := pageForPlaybackPosition(plan, resolver.Record, 7, 0)
	if page != 42 {
		t.Fatalf("streamed PDF sequence mapped to page %d; want resolver page 42", page)
	}
}

func TestPlaybackRateChangeAppliesLiveAndPersistsForNextSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	userPaths, err := paths.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	config := settings.Defaults()
	config.PlaybackRate = 1.2
	if err := settings.Save(context.Background(), userPaths.ConfigDir, config); err != nil {
		t.Fatal(err)
	}
	plan := segment.Plan{DocumentID: "book", Segments: []segment.Segment{{ID: "opening", Seq: 0, SourceText: "Opening passage.", SpokenText: "Opening passage."}}}
	screen := &fakePlayerScreen{actions: make(chan playerui.Action, 2), started: make(chan struct{}, 1), statuses: make(chan string, 8)}
	player := &liveRateTestPlayer{started: make(chan struct{}), rates: make(chan float64, 2), closed: make(chan struct{})}
	deps := runtimeDeps{newPlayer: func(options playback.MPVOptions) playback.Player {
		if options.PlaybackSpeed != 1.2 {
			t.Errorf("player started at rate %.1f, want persisted default 1.2", options.PlaybackSpeed)
		}
		return player
	}}.withDefaults()
	result := make(chan playbackDecision, 1)
	go func() {
		result <- runPlaybackPass(context.Background(), io.Discard, deps,
			listenOptions{playbackSpeed: 1.2}, plan, plan, 0, 0,
			[]playerui.Chapter{{ID: "opening", Title: "Opening", StartIndex: 0, StartSeq: 0}},
			&testkit.FakeSynthesizer{Duration: time.Second}, nil, pipeline.StreamOptions{StartupBuffer: time.Nanosecond},
			nil, session.State{}, screen, nil, nil)
	}()
	awaitSignal(t, screen.started, "playback starts before rate adjustment")
	screen.actions <- playerui.Action{Kind: playerui.ActionPlaybackRate, Rate: 1.7}
	select {
	case rate := <-player.rates:
		if rate != 1.7 {
			t.Fatalf("live player rate = %.1f, want 1.7", rate)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("player did not receive the live playback-rate change")
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case status := <-screen.statuses:
			if strings.Contains(status, "saved for next session") {
				goto saved
			}
		case <-deadline:
			t.Fatal("playback-rate preference was not saved after the live update")
		}
	}
saved:
	got, found, err := settings.Load(userPaths.ConfigDir)
	if err != nil || !found || got.PlaybackRate != 1.7 {
		t.Fatalf("persisted playback rate = %.1f, found=%v, err=%v", got.PlaybackRate, found, err)
	}
	screen.actions <- playerui.Action{Kind: playerui.ActionStop}
	select {
	case decision := <-result:
		if decision.err != nil || !decision.stop {
			t.Fatalf("stop after rate change = %#v", decision)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("playback did not stop after live rate change")
	}
}

type liveRateTestPlayer struct {
	started chan struct{}
	rates   chan float64
	closed  chan struct{}
	close   sync.Once
}

func (p *liveRateTestPlayer) Start(context.Context) error {
	close(p.started)
	return nil
}

func (*liveRateTestPlayer) Append(ctx context.Context, _ playback.Item) error { return ctx.Err() }

func (*liveRateTestPlayer) Position(context.Context) (uint64, time.Duration, error) { return 0, 0, nil }

func (*liveRateTestPlayer) Wait(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func (p *liveRateTestPlayer) SetPlaybackRate(ctx context.Context, rate float64) error {
	select {
	case p.rates <- rate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *liveRateTestPlayer) Close() error {
	p.close.Do(func() { close(p.closed) })
	return nil
}

var _ playback.RateSetter = (*liveRateTestPlayer)(nil)

func TestLivePlaybackFinishesQueuedAudioBeforeReportingLaterPageError(t *testing.T) {
	pageErr := errors.New("later PDF page OCR failed")
	player := &drainingErrorPlayer{waiting: make(chan struct{}), release: make(chan struct{})}
	deps := runtimeDeps{
		interactivePlayback: func(io.Reader, io.Writer) bool { return false },
		newPlayer:           func(playback.MPVOptions) playback.Player { return player },
	}
	plan := segment.Plan{DocumentID: "pdf", Segments: []segment.Segment{{ID: "pending", Seq: 0}}}
	liveProducer := func(ctx context.Context, emit func(segment.Segment) error, _ func() error, _ func(string)) error {
		if err := emit(segment.Segment{ID: "opening", Seq: 0, SpokenText: "opening page"}); err != nil {
			return err
		}
		return pageErr
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
			listenOptions{playbackSpeed: 1}, "Scanned PDF", plan,
			playerui.StartPoint{StartIndex: 0, StartSeq: 0},
			[]playerui.Chapter{{ID: "pdf", Title: "Reading order", StartIndex: 0, StartSeq: 0}},
			&testkit.FakeSynthesizer{Duration: time.Second}, nil,
			pipeline.StreamOptions{StartupBuffer: time.Nanosecond}, nil, session.State{}, nil, liveProducer, nil)
		done <- err
	}()
	awaitSignal(t, player.waiting, "ready audio drain")
	select {
	case err := <-done:
		t.Fatalf("playback reported the OCR error before ready audio drained: %v", err)
	default:
	}
	close(player.release)
	select {
	case err := <-done:
		if !errors.Is(err, pageErr) {
			t.Fatalf("playback error = %v, want the later page OCR failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("playback did not report the OCR error after audio drained")
	}
}

type drainingErrorPlayer struct {
	waiting chan struct{}
	release chan struct{}
}

func (*drainingErrorPlayer) Start(context.Context) error                       { return nil }
func (*drainingErrorPlayer) Append(ctx context.Context, _ playback.Item) error { return ctx.Err() }
func (*drainingErrorPlayer) Position(context.Context) (uint64, time.Duration, error) {
	return 0, 0, nil
}
func (p *drainingErrorPlayer) Wait(ctx context.Context) error {
	close(p.waiting)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (*drainingErrorPlayer) Close() error { return nil }

func awaitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func awaitItem(t *testing.T, items <-chan playback.Item, sequence uint64) {
	t.Helper()
	select {
	case item := <-items:
		if item.Seq != sequence {
			t.Fatalf("queued sequence = %d, want %d", item.Seq, sequence)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for queued sequence %d", sequence)
	}
}

func awaitSignalValue[T comparable](t *testing.T, values <-chan T, expected T, description string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got := <-values:
			if got == expected {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		}
	}
}

func awaitPosition(t *testing.T, positions <-chan playback.Position, expected playback.Position) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got := <-positions:
			if got.Seq == expected.Seq && got.Offset == expected.Offset && got.Duration == expected.Duration {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for seek position %#v", expected)
		}
	}
}

type appendedSegment struct {
	player int
	seq    uint64
}

type bufferedSeekTestPlayer struct {
	mu            sync.Mutex
	items         []playback.Item
	current       playback.Position
	paused        bool
	appended      chan playback.Item
	seekDeadlines chan time.Time
}

func (*bufferedSeekTestPlayer) Start(context.Context) error { return nil }

func (p *bufferedSeekTestPlayer) Append(ctx context.Context, item playback.Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.items = append(p.items, item)
	if len(p.items) == 1 {
		p.current = playback.Position{Seq: item.Seq, Duration: item.Duration}
	}
	p.mu.Unlock()
	select {
	case p.appended <- item:
	default:
	}
	return nil
}

func (p *bufferedSeekTestPlayer) Position(context.Context) (uint64, time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current.Seq, p.current.Offset, nil
}

func (p *bufferedSeekTestPlayer) CurrentDuration(context.Context) (time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current.Duration, nil
}

func (p *bufferedSeekTestPlayer) SeekBy(ctx context.Context, delta time.Duration) (playback.Position, error) {
	if err := ctx.Err(); err != nil {
		return playback.Position{}, err
	}
	if deadline, ok := ctx.Deadline(); ok && p.seekDeadlines != nil {
		select {
		case p.seekDeadlines <- deadline:
		default:
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.items) < 2 {
		return playback.Position{}, playback.ErrSeekNotBuffered
	}
	p.current = playback.Position{Seq: 1, Offset: 2 * time.Second, Duration: 3 * time.Second}
	return p.current, nil
}

func (p *bufferedSeekTestPlayer) SetPaused(ctx context.Context, paused bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.paused = paused
	p.mu.Unlock()
	return nil
}

func (*bufferedSeekTestPlayer) Wait(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
func (*bufferedSeekTestPlayer) Close() error                   { return nil }

func (p *bufferedSeekTestPlayer) isPaused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

type navigationTestPlayer struct {
	id            int
	appended      chan<- appendedSegment
	blockAppend   bool
	blockSequence uint64
	blockStarted  chan struct{}
	mu            sync.Mutex
	closed        int
}

func (*navigationTestPlayer) Start(context.Context) error { return nil }

func (p *navigationTestPlayer) Append(ctx context.Context, item playback.Item) error {
	if p.blockAppend && item.Seq == p.blockSequence {
		close(p.blockStarted)
		<-ctx.Done()
		return errors.New("mpv IPC transport closed while the old chapter was replaced")
	}
	select {
	case p.appended <- appendedSegment{player: p.id, seq: item.Seq}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*navigationTestPlayer) Position(context.Context) (uint64, time.Duration, error) {
	return 0, 0, context.DeadlineExceeded
}

func (p *navigationTestPlayer) Wait(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }

func (p *navigationTestPlayer) Close() error {
	p.mu.Lock()
	p.closed++
	p.mu.Unlock()
	return nil
}

func (p *navigationTestPlayer) closeCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

type fakePlayerScreen struct {
	actions     chan playerui.Action
	started     chan struct{}
	statuses    chan string
	seekPending chan time.Duration
	pauseStates chan bool
	positions   chan playback.Position
}

func (s *fakePlayerScreen) Actions() <-chan playerui.Action { return s.actions }
func (s *fakePlayerScreen) SetStatus(status string) {
	select {
	case s.statuses <- status:
	default:
	}
}
func (*fakePlayerScreen) SetProgress(int, int)    {}
func (*fakePlayerScreen) SetChapter(int)          {}
func (*fakePlayerScreen) SetPage(int)             {}
func (*fakePlayerScreen) SetPlaybackRate(float64) {}
func (s *fakePlayerScreen) SetPaused(paused bool) {
	select {
	case s.pauseStates <- paused:
	default:
	}
}
func (s *fakePlayerScreen) SetSeekPending(delta time.Duration) {
	select {
	case s.seekPending <- delta:
	default:
	}
}
func (s *fakePlayerScreen) SetPlaybackStarted() { s.started <- struct{}{} }
func (s *fakePlayerScreen) SetPosition(sequence uint64, offset, duration time.Duration) {
	select {
	case s.positions <- playback.Position{Seq: sequence, Offset: offset, Duration: duration}:
	default:
	}
}
func (*fakePlayerScreen) SetFinished(string) {}
func (*fakePlayerScreen) Close()             {}

var _ playerScreen = (*fakePlayerScreen)(nil)
