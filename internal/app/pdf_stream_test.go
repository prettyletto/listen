package app

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

func TestNearestPDFPagePassagePrefersSavedPageAndClampsPageSlot(t *testing.T) {
	pages := []pdfPageSegments{
		{Page: 2, Segments: []segment.Segment{
			{ID: "p2a", Seq: 0, Source: domain.SourceRef{Page: 2}},
			{ID: "p2b", Seq: 1, Source: domain.SourceRef{Page: 2}},
		}},
		{Page: 5, Segments: []segment.Segment{
			{ID: "p5a", Seq: 2, Source: domain.SourceRef{Page: 5}},
		}},
	}
	point := nearestPDFPagePassage(pages, 2, 1)
	if !point.Found || point.Segment.ID != "p2b" {
		t.Fatalf("same-page resolution = %#v", point)
	}
	point = nearestPDFPagePassage(pages, 4, 20)
	if !point.Found || point.Segment.ID != "p5a" {
		t.Fatalf("nearest non-empty page resolution = %#v", point)
	}
	point = nearestPDFPagePassage(pages, 3, 0)
	if !point.Found || point.Segment.ID != "p2a" {
		t.Fatalf("equidistant page resolution = %#v, want earlier page on tie", point)
	}
}

func TestNearestPDFSequencePassageUsesClosestStableSequence(t *testing.T) {
	pages := []pdfPageSegments{{Page: 1, Segments: []segment.Segment{
		{ID: "one", Seq: 4}, {ID: "two", Seq: 7}, {ID: "three", Seq: 10},
	}}}
	point := nearestPDFSequencePassage(pages, 8)
	if !point.Found || point.Segment.ID != "two" {
		t.Fatalf("sequence resolution = %#v", point)
	}
}

func TestPDFFallbackPreservesOffsetOnlyWhenDocumentIDMatches(t *testing.T) {
	location := pdfResumeLocation{Segment: segment.Segment{ID: "replacement", Source: domain.SourceRef{Page: 8}}, Found: true}
	saved := session.State{DocumentID: "same-bytes", Offset: 1700 * time.Millisecond}
	got := fallbackPDFResume(location, saved, true)
	if got.Offset != saved.Offset || got.Exact || got.ChangedContent {
		t.Fatalf("same-document page fallback = %#v", got)
	}
	got = fallbackPDFResume(location, saved, false)
	if got.Offset != 0 || got.Exact || !got.ChangedContent {
		t.Fatalf("changed-content page fallback = %#v", got)
	}
}

func TestPDFRestoreEscapeCancelsMappingAndReturnsHome(t *testing.T) {
	screen := &fakePlayerScreen{actions: make(chan playerui.Action, 1)}
	started := make(chan struct{})
	done := make(chan struct {
		end       PlaybackEnd
		cancelled bool
		err       error
	}, 1)
	go func() {
		_, end, cancelled, err := awaitPDFRestore(context.Background(), screen, func(ctx context.Context, _ func(string)) (pdfResumeLocation, error) {
			close(started)
			<-ctx.Done()
			return pdfResumeLocation{}, ctx.Err()
		})
		done <- struct {
			end       PlaybackEnd
			cancelled bool
			err       error
		}{end: end, cancelled: cancelled, err: err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("restore mapping did not start")
	}
	screen.actions <- playerui.Action{Kind: playerui.ActionReturnHome}
	select {
	case result := <-done:
		if !result.cancelled || result.end != PlaybackReturnedHome || result.err != nil {
			t.Fatalf("restore cancellation = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("Escape did not cancel the restore mapping")
	}
}

func TestScannedPDFResumeSkipsEarlierPageAfterInterruption(t *testing.T) {
	for _, name := range []string{"pdftohtml", "pdfimages", "pdftoppm", "tesseract"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("Poppler or Tesseract OCR tools are not installed")
		}
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	for _, path := range []string{home, filepath.Join(root, "cache"), filepath.Join(root, "config")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	pdfPath, err := filepath.Abs("../../testdata/pdf/scanned.pdf")
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(root, "cache", "listen")
	store, err := session.NewStore(cacheRoot, pdfPath)
	if err != nil {
		t.Fatal(err)
	}

	first := runScannedPDFTestPlayback(t, pdfPath, 1350*time.Millisecond)
	firstPageOne := waitPDFPlayerAppend(t, first.player)
	firstPageTwo := waitPDFPlayerAppend(t, first.player)
	if firstPageOne.Seq != 0 || firstPageTwo.Seq != 1 {
		t.Fatalf("initial playback order = %d, %d; want 0, 1", firstPageOne.Seq, firstPageTwo.Seq)
	}
	first.screen.actions <- playerui.Action{Kind: playerui.ActionReturnHome}
	waitPDFPlaybackResult(t, first.done)
	saved, found, err := store.Load(context.Background())
	if err != nil || !found {
		t.Fatalf("load interrupted PDF position: found=%v err=%v", found, err)
	}
	if saved.SegmentSeq != 1 || saved.Page != 2 || saved.Offset != 1350*time.Millisecond || saved.SegmentID == "" {
		t.Fatalf("saved scanned-PDF position = %#v, want page 2 / segment 1", saved)
	}
	if got := first.player.itemsSnapshot(); len(got) < 2 || got[0].Seq != 0 || got[1].Seq != 1 {
		t.Fatalf("first playback did not reach page two in order: %#v", got)
	}

	second := runScannedPDFTestPlayback(t, pdfPath, 1350*time.Millisecond)
	secondItem := waitPDFPlayerAppend(t, second.player)
	if secondItem.Seq != 1 {
		t.Fatalf("resumed playback appended sequence %d before saved sequence 1", secondItem.Seq)
	}
	if got := second.player.resumeOffset(); got != 1350*time.Millisecond {
		t.Fatalf("resumed passage offset = %s, want %s", got, 1350*time.Millisecond)
	}
	second.screen.actions <- playerui.Action{Kind: playerui.ActionReturnHome}
	waitPDFPlaybackResult(t, second.done)
	if got := second.player.resumeOffset(); got != 1350*time.Millisecond {
		t.Fatalf("resumed passage offset = %v, want %v", got, 1350*time.Millisecond)
	}
	items := second.player.itemsSnapshot()
	if len(items) != 1 || items[0].Seq != 1 {
		t.Fatalf("resume replayed an earlier page or appended out of order: %#v", items)
	}
}

type pdfTestPlayback struct {
	screen *fakePlayerScreen
	player *pdfResumeTestPlayer
	done   chan error
}

func runScannedPDFTestPlayback(t *testing.T, pdfPath string, offset time.Duration) pdfTestPlayback {
	t.Helper()
	screen := &fakePlayerScreen{actions: make(chan playerui.Action, 4), started: make(chan struct{}, 1), seekPending: make(chan time.Duration, 2), pauseStates: make(chan bool, 2), positions: make(chan playback.Position, 2)}
	playerReady := make(chan *pdfResumeTestPlayer, 1)
	deps := runtimeDeps{
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) { return earlyOCRTestSynth{t: t}, nil },
		newPlayer: func(playback.MPVOptions) playback.Player {
			player := &pdfResumeTestPlayer{offset: offset, added: make(chan playback.Item, 8)}
			playerReady <- player
			return player
		},
		interactivePlayback: func(io.Reader, io.Writer) bool { return true },
		startPlaybackScreen: func(io.Reader, io.Writer, playerui.Model) (playerScreen, error) { return screen, nil },
		startupBuffer:       time.Nanosecond, sessionPollInterval: 5 * time.Millisecond,
	}.withDefaults()
	done := make(chan error, 1)
	go func() {
		options := listenOptions{source: pdfPath, voice: "af_heart", speed: 1, playbackSpeed: 1}
		done <- listenSource(context.Background(), options, strings.NewReader(""), io.Discard, deps)
	}()
	var player *pdfResumeTestPlayer
	select {
	case player = <-playerReady:
	case err := <-done:
		t.Fatalf("PDF playback ended before player start: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("PDF playback player did not start")
	}
	return pdfTestPlayback{screen: screen, player: player, done: done}
}

func waitPDFPlayerAppend(t *testing.T, player *pdfResumeTestPlayer) playback.Item {
	t.Helper()
	select {
	case item := <-player.added:
		return item
	case <-time.After(10 * time.Second):
		t.Fatal("PDF playback did not append ready audio")
		return playback.Item{}
	}
}

func waitPDFPlaybackResult(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("scanned PDF playback: %v (cause: %v)", err, errors.Unwrap(err))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scanned PDF playback did not stop")
	}
}

type pdfResumeTestPlayer struct {
	mu     sync.Mutex
	items  []playback.Item
	offset time.Duration
	resume time.Duration
	added  chan playback.Item
}

func (*pdfResumeTestPlayer) Start(context.Context) error { return nil }
func (p *pdfResumeTestPlayer) Append(ctx context.Context, item playback.Item) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.items = append(p.items, item)
	p.mu.Unlock()
	select {
	case p.added <- item:
	default:
	}
	return nil
}
func (p *pdfResumeTestPlayer) AppendAt(ctx context.Context, item playback.Item, offset time.Duration) error {
	p.mu.Lock()
	p.resume = offset
	p.mu.Unlock()
	return p.Append(ctx, item)
}
func (p *pdfResumeTestPlayer) Position(ctx context.Context) (uint64, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.items) == 0 {
		return 0, 0, errors.New("no audio is queued")
	}
	return p.items[len(p.items)-1].Seq, p.offset, nil
}
func (*pdfResumeTestPlayer) Wait(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
func (*pdfResumeTestPlayer) Close() error { return nil }
func (p *pdfResumeTestPlayer) itemsSnapshot() []playback.Item {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]playback.Item(nil), p.items...)
}
func (p *pdfResumeTestPlayer) resumeOffset() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resume
}

var _ playback.Player = (*pdfResumeTestPlayer)(nil)
var _ playback.OffsetAppender = (*pdfResumeTestPlayer)(nil)
