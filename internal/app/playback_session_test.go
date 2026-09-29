package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/pipeline"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
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
		report, stopped, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
			listenOptions{playbackSpeed: 1}, "Example Book", plan,
			playerui.StartPoint{StartIndex: 0, StartSeq: 0, ChapterIndex: 0}, chapters,
			goSynth, nil, pipeline.StreamOptions{StartupBuffer: time.Nanosecond}, nil, session.State{}, nil)
		result <- struct {
			report  pipeline.StreamReport
			stopped bool
			err     error
		}{report: report, stopped: stopped, err: err}
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
		_, stopped, err := runPlayback(context.Background(), strings.NewReader(""), io.Discard, deps,
			listenOptions{playbackSpeed: 1}, "Example Book", plan,
			playerui.StartPoint{StartIndex: 0, StartSeq: 0, ChapterIndex: 0}, chapters,
			synth, nil, pipeline.StreamOptions{Workers: 1, StartupBuffer: time.Nanosecond}, nil, session.State{}, nil)
		result <- struct {
			stopped bool
			err     error
		}{stopped: stopped, err: err}
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
	seekPending chan time.Duration
	pauseStates chan bool
	positions   chan playback.Position
}

func (s *fakePlayerScreen) Actions() <-chan playerui.Action { return s.actions }
func (*fakePlayerScreen) SetStatus(string)                  {}
func (*fakePlayerScreen) SetProgress(int, int)              {}
func (*fakePlayerScreen) SetChapter(int)                    {}
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
