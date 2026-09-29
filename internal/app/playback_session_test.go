package app

import (
	"context"
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

type appendedSegment struct {
	player int
	seq    uint64
}

type navigationTestPlayer struct {
	id       int
	appended chan<- appendedSegment
	mu       sync.Mutex
	closed   int
}

func (*navigationTestPlayer) Start(context.Context) error { return nil }

func (p *navigationTestPlayer) Append(ctx context.Context, item playback.Item) error {
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
	actions chan playerui.Action
	started chan struct{}
}

func (s *fakePlayerScreen) Actions() <-chan playerui.Action                { return s.actions }
func (*fakePlayerScreen) SetStatus(string)                                 {}
func (*fakePlayerScreen) SetProgress(int, int)                             {}
func (*fakePlayerScreen) SetChapter(int)                                   {}
func (*fakePlayerScreen) SetPaused(bool)                                   {}
func (s *fakePlayerScreen) SetPlaybackStarted()                            { s.started <- struct{}{} }
func (*fakePlayerScreen) SetPosition(uint64, time.Duration, time.Duration) {}
func (*fakePlayerScreen) SetFinished(string)                               {}
func (*fakePlayerScreen) Close()                                           {}

var _ playerScreen = (*fakePlayerScreen)(nil)
