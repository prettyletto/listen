package session

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/segment"
)

func TestStoreRoundTripsStateAndPreservesCheckpointOnCanceledWrite(t *testing.T) {
	store, err := NewStore(t.TempDir(), filepath.Join(t.TempDir(), "book.epub"))
	if err != nil {
		t.Fatal(err)
	}
	want := State{DocumentID: "doc", SegmentID: "segment", SegmentSeq: 4, Offset: 1500 * time.Millisecond, Voice: "af_heart", TargetLanguage: "pt-BR", TranslationBackend: "local-test-v1"}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Load(context.Background())
	if err != nil || !found {
		t.Fatalf("load found=%v err=%v", found, err)
	}
	if got.DocumentID != want.DocumentID || got.SegmentID != want.SegmentID || got.SegmentSeq != want.SegmentSeq || got.Offset != want.Offset || got.Voice != want.Voice || got.TargetLanguage != want.TargetLanguage || got.TranslationBackend != want.TranslationBackend {
		t.Fatalf("loaded state = %#v, want %#v", got, want)
	}
	if got.Version != manifestVersion || got.UpdatedAt.IsZero() {
		t.Fatalf("state metadata was not populated: %#v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.Save(ctx, State{DocumentID: "replacement"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled save error = %v, want context.Canceled", err)
	}
	got, found, err = store.Load(context.Background())
	if err != nil || !found || got.DocumentID != want.DocumentID {
		t.Fatalf("canceled write damaged checkpoint: found=%v state=%#v err=%v", found, got, err)
	}
}

func TestResolveUsesStableSegmentThenStructuralFallback(t *testing.T) {
	plan := segment.Plan{DocumentID: "doc", Segments: []segment.Segment{
		{ID: "opening", Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Book", "Chapter 1"}},
		{ID: "current", Seq: 1, MajorUnitID: "chapter-1", SectionPath: []string{"Book", "Chapter 1"}},
		{ID: "next", Seq: 2, MajorUnitID: "chapter-2", SectionPath: []string{"Book", "Chapter 2"}},
	}}
	point, ok := Resolve(plan, State{DocumentID: "doc", SegmentID: "current", SegmentSeq: 19, Offset: 3 * time.Second})
	if !ok || !point.Exact || point.Index != 1 || point.Seq != 1 || point.Offset != 3*time.Second {
		t.Fatalf("stable resolution = %#v, ok=%v", point, ok)
	}
	point, ok = Resolve(plan, State{DocumentID: "changed-doc", SegmentID: "current", SegmentSeq: 19, Offset: 3 * time.Second})
	if !ok || point.Exact || point.Index != 1 || point.Offset != 0 {
		t.Fatalf("changed source reused passage offset = %#v, ok=%v", point, ok)
	}
	point, ok = Resolve(plan, State{SegmentID: "edited", SegmentSeq: 10, MajorUnitID: "chapter-2", Offset: 3 * time.Second})
	if !ok || point.Exact || point.Index != 2 || point.Offset != 0 {
		t.Fatalf("structural resolution = %#v, ok=%v", point, ok)
	}
	if _, ok := Resolve(plan, State{SegmentID: "current", Complete: true}); ok {
		t.Fatal("completed session should not resume")
	}
}

func TestPositionResolverRecordsStreamedPDFPageAndPageSlot(t *testing.T) {
	resolver := NewPositionResolver()
	resolver.Add(segment.Segment{ID: "page-4-a", Seq: 9, Source: domain.SourceRef{Page: 4}}, 0)
	resolver.Add(segment.Segment{ID: "page-4-b", Seq: 10, Source: domain.SourceRef{Page: 4}}, 1)
	state, ok := resolver.Record(State{DocumentID: "book"}, 10, 2300*time.Millisecond)
	if !ok {
		t.Fatal("resolver did not find the emitted sequence")
	}
	if state.SegmentID != "page-4-b" || state.SegmentSeq != 10 || state.Page != 4 || state.PageSegment != 1 || state.Offset != 2300*time.Millisecond {
		t.Fatalf("stream checkpoint = %#v", state)
	}
}

func TestPositionResolverCanRecordWhilePagesArePublished(t *testing.T) {
	resolver := NewPositionResolver()
	var readers sync.WaitGroup
	for sequence := uint64(0); sequence < 100; sequence++ {
		readers.Add(1)
		go func(sequence uint64) {
			defer readers.Done()
			resolver.Add(segment.Segment{ID: "passage", Seq: sequence, Source: domain.SourceRef{Page: int(sequence/10) + 1}}, int(sequence%10))
			_, _ = resolver.Record(State{}, sequence, time.Second)
		}(sequence)
	}
	readers.Wait()
	for sequence := uint64(0); sequence < 100; sequence++ {
		if _, ok := resolver.Record(State{}, sequence, time.Second); !ok {
			t.Fatalf("resolver lost sequence %d", sequence)
		}
	}
}

func TestResolveScannedPDFUsesSavedPageBeforeGlobalSequence(t *testing.T) {
	plan := segment.Plan{DocumentID: "new-doc", Segments: []segment.Segment{
		{ID: "p3a", Seq: 0, Source: domain.SourceRef{Page: 3}},
		{ID: "p3b", Seq: 1, Source: domain.SourceRef{Page: 3}},
		{ID: "p6a", Seq: 2, Source: domain.SourceRef{Page: 6}},
	}}
	point, ok := Resolve(plan, State{DocumentID: "old-doc", SegmentID: "gone", SegmentSeq: 0, Page: 3, PageSegment: 1, Offset: 4 * time.Second})
	if !ok || point.Index != 1 || point.Seq != 1 || point.Offset != 0 || point.Exact {
		t.Fatalf("changed PDF page resume = %#v, ok=%v", point, ok)
	}
	point, ok = Resolve(plan, State{DocumentID: "new-doc", SegmentID: "moved", SegmentSeq: 0, Page: 3, PageSegment: 1, Offset: 4 * time.Second})
	if !ok || point.Index != 1 || point.Offset != 4*time.Second || point.Exact {
		t.Fatalf("same-content page fallback = %#v, ok=%v", point, ok)
	}
	point, ok = Resolve(plan, State{DocumentID: "old-doc", SegmentID: "gone", SegmentSeq: 1, Page: 4, PageSegment: 0})
	if !ok || point.Index != 0 || point.Offset != 0 {
		t.Fatalf("nearest non-empty page fallback = %#v, ok=%v", point, ok)
	}
}

func TestMonitorFlushesPositionOnCancellation(t *testing.T) {
	cacheRoot := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "book.txt")
	store, err := NewStore(cacheRoot, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := segment.Plan{DocumentID: "doc", Segments: []segment.Segment{{
		ID: "segment-4", Seq: 4, MajorUnitID: "chapter-1", SectionPath: []string{"Book", "Chapter 1"},
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	player := fixedPosition{seq: 4, offset: 2200 * time.Millisecond}
	monitor := StartMonitor(ctx, player, store, State{DocumentID: "doc"}, plan, time.Hour)
	if monitor == nil {
		t.Fatal("monitor was not started")
	}
	cancel()
	if err := monitor.Stop(); err != nil {
		t.Fatal(err)
	}
	state, found, err := store.Load(context.Background())
	if err != nil || !found {
		t.Fatalf("load monitored state found=%v err=%v", found, err)
	}
	if state.SegmentID != "segment-4" || state.SegmentSeq != 4 || state.Offset != player.offset || state.MajorUnitID != "chapter-1" {
		t.Fatalf("monitor saved %#v", state)
	}
}

func TestSessionMonitorSurvivesForcedProcessExit(t *testing.T) {
	if cacheRoot := os.Getenv("LISTEN_SESSION_CHILD_CACHE"); cacheRoot != "" {
		sourcePath := os.Getenv("LISTEN_SESSION_CHILD_SOURCE")
		store, err := NewStore(cacheRoot, sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		plan := segment.Plan{Segments: []segment.Segment{{
			ID: "stable-segment", Seq: 7, MajorUnitID: "chapter-2", SectionPath: []string{"Book", "Chapter 2"},
		}}}
		StartMonitor(context.Background(), fixedPosition{seq: 7, offset: 4 * time.Second}, store, State{DocumentID: "doc"}, plan, 10*time.Millisecond)
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, found, _ := store.Load(context.Background()); found {
				select {}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("child monitor did not persist a checkpoint")
	}

	cacheRoot := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "book.epub")
	store, err := NewStore(cacheRoot, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestSessionMonitorSurvivesForcedProcessExit$")
	command.Env = append(os.Environ(), "LISTEN_SESSION_CHILD_CACHE="+cacheRoot, "LISTEN_SESSION_CHILD_SOURCE="+sourcePath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var state State
	var found bool
	var loadErr error
	for time.Now().Before(deadline) {
		state, found, loadErr = store.Load(context.Background())
		if loadErr != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal(loadErr)
		}
		if found {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state.SegmentID == "" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("child did not persist its playback position")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()

	state, found, loadErr = store.Load(context.Background())
	if loadErr != nil || !found {
		t.Fatalf("load after forced process exit found=%v err=%v", found, loadErr)
	}
	plan := segment.Plan{DocumentID: "doc", Segments: []segment.Segment{{
		ID: "stable-segment", Seq: 7, MajorUnitID: "chapter-2", SectionPath: []string{"Book", "Chapter 2"},
	}}}
	point, ok := Resolve(plan, state)
	if !ok || !point.Exact || point.Seq != 7 || point.Offset != 4*time.Second {
		t.Fatalf("resume after forced process exit = %#v, ok=%v state=%#v", point, ok, state)
	}
}

func TestStreamedPDFMonitorSurvivesForcedProcessExit(t *testing.T) {
	const childCacheEnv = "LISTEN_SESSION_STREAM_CHILD_CACHE"
	if cacheRoot := os.Getenv(childCacheEnv); cacheRoot != "" {
		sourcePath := os.Getenv("LISTEN_SESSION_STREAM_CHILD_SOURCE")
		store, err := NewStore(cacheRoot, sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		resolver := NewPositionResolver()
		resolver.Add(segment.Segment{ID: "page-3-second-passage", Seq: 12, Source: domain.SourceRef{Page: 3}}, 1)
		monitor := StartMonitorWithRecorder(context.Background(), fixedPosition{seq: 12, offset: 4300 * time.Millisecond}, store,
			State{DocumentID: "scanned-pdf"}, resolver.Record, 10*time.Millisecond)
		if monitor == nil {
			t.Fatal("streamed PDF checkpoint monitor was not started")
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if state, found, _ := store.Load(context.Background()); found && state.Page == 3 && state.SegmentID != "" {
				select {}
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("child monitor did not persist a streamed PDF checkpoint")
	}

	cacheRoot := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "scanned.pdf")
	store, err := NewStore(cacheRoot, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestStreamedPDFMonitorSurvivesForcedProcessExit$")
	command.Env = append(os.Environ(), childCacheEnv+"="+cacheRoot, "LISTEN_SESSION_STREAM_CHILD_SOURCE="+sourcePath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var state State
	var found bool
	for time.Now().Before(deadline) {
		state, found, err = store.Load(context.Background())
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal(err)
		}
		if found {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !found {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("child did not persist its streamed PDF position")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()

	state, found, err = store.Load(context.Background())
	if err != nil || !found {
		t.Fatalf("load streamed PDF checkpoint after forced exit found=%v err=%v", found, err)
	}
	if state.SegmentID != "page-3-second-passage" || state.SegmentSeq != 12 || state.Page != 3 || state.PageSegment != 1 || state.Offset != 4300*time.Millisecond {
		t.Fatalf("forced-exit checkpoint = %#v", state)
	}
	plan := segment.Plan{DocumentID: state.DocumentID, Segments: []segment.Segment{{
		ID: state.SegmentID, Seq: state.SegmentSeq, Source: domain.SourceRef{Page: state.Page},
	}}}
	point, ok := Resolve(plan, state)
	if !ok || !point.Exact || point.Seq != 12 || point.Offset != 4300*time.Millisecond {
		t.Fatalf("restored streamed PDF position = %#v, ok=%v", point, ok)
	}
}

type fixedPosition struct {
	seq    uint64
	offset time.Duration
}

func (p fixedPosition) Position(context.Context) (uint64, time.Duration, error) {
	return p.seq, p.offset, nil
}
