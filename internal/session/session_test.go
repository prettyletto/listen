package session

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

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
	plan := segment.Plan{Segments: []segment.Segment{
		{ID: "opening", Seq: 0, MajorUnitID: "chapter-1", SectionPath: []string{"Book", "Chapter 1"}},
		{ID: "current", Seq: 1, MajorUnitID: "chapter-1", SectionPath: []string{"Book", "Chapter 1"}},
		{ID: "next", Seq: 2, MajorUnitID: "chapter-2", SectionPath: []string{"Book", "Chapter 2"}},
	}}
	point, ok := Resolve(plan, State{SegmentID: "current", SegmentSeq: 19, Offset: 3 * time.Second})
	if !ok || !point.Exact || point.Index != 1 || point.Seq != 1 || point.Offset != 3*time.Second {
		t.Fatalf("stable resolution = %#v, ok=%v", point, ok)
	}
	point, ok = Resolve(plan, State{SegmentID: "edited", SegmentSeq: 10, MajorUnitID: "chapter-2", Offset: 3 * time.Second})
	if !ok || point.Exact || point.Index != 2 || point.Offset != 0 {
		t.Fatalf("structural resolution = %#v, ok=%v", point, ok)
	}
	if _, ok := Resolve(plan, State{SegmentID: "current", Complete: true}); ok {
		t.Fatal("completed session should not resume")
	}
}

func TestMonitorFlushesPositionOnCancellation(t *testing.T) {
	cacheRoot := t.TempDir()
	sourcePath := filepath.Join(t.TempDir(), "book.txt")
	store, err := NewStore(cacheRoot, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	plan := segment.Plan{Segments: []segment.Segment{{
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
	plan := segment.Plan{Segments: []segment.Segment{{
		ID: "stable-segment", Seq: 7, MajorUnitID: "chapter-2", SectionPath: []string{"Book", "Chapter 2"},
	}}}
	point, ok := Resolve(plan, state)
	if !ok || !point.Exact || point.Seq != 7 || point.Offset != 4*time.Second {
		t.Fatalf("resume after forced process exit = %#v, ok=%v state=%#v", point, ok, state)
	}
}

type fixedPosition struct {
	seq    uint64
	offset time.Duration
}

func (p fixedPosition) Position(context.Context) (uint64, time.Duration, error) {
	return p.seq, p.offset, nil
}
