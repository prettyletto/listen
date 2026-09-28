package app

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/source"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

func TestAppResumesAfterForcedProcessExit(t *testing.T) {
	if os.Getenv("LISTEN_APP_RESUME_CHILD") == "1" {
		targetSeq, err := strconv.ParseUint(os.Getenv("LISTEN_APP_RESUME_TARGET_SEQ"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		sourcePath := os.Getenv("LISTEN_APP_RESUME_SOURCE")
		deps := runtimeDeps{
			startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
				return resumeTestSynth{}, nil
			},
			newPlayer: func(playback.MPVOptions) playback.Player {
				return &resumeTestPlayer{targetSeq: targetSeq, offset: 1250 * time.Millisecond, blockWait: true}
			},
			startupBuffer:       time.Nanosecond,
			sessionPollInterval: 5 * time.Millisecond,
		}
		if err := runWithRuntime(context.Background(), []string{sourcePath}, nil, io.Discard, io.Discard, deps); err != nil {
			t.Fatal(err)
		}
		t.Fatal("child app returned before the forced process exit")
	}

	root := t.TempDir()
	home := filepath.Join(root, "home")
	cacheHome := filepath.Join(root, "xdg-cache")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	sourcePath := filepath.Join(home, "resume.txt")
	paragraph := strings.Repeat("steady voices carry clear stories across the quiet evening ", 15)
	content := strings.Join([]string{paragraph, paragraph, paragraph}, "\n\n")
	if err := os.WriteFile(sourcePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := parseDocument(context.Background(), source.Input{Name: filepath.Base(sourcePath), Path: sourcePath})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := segment.PlanDocument(doc, segment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) < 2 {
		t.Fatalf("test source produced only %d segments", len(plan.Segments))
	}
	target := plan.Segments[1]
	const wantOffset = 1250 * time.Millisecond
	store, err := session.NewStore(filepath.Join(cacheHome, "listen"), sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestAppResumesAfterForcedProcessExit$")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.Env = append(os.Environ(),
		"LISTEN_APP_RESUME_CHILD=1",
		"LISTEN_APP_RESUME_SOURCE="+sourcePath,
		"LISTEN_APP_RESUME_TARGET_SEQ="+strconv.FormatUint(target.Seq, 10),
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	childRunning := true
	defer func() {
		if childRunning {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()

	deadline := time.Now().Add(8 * time.Second)
	var saved session.State
	for time.Now().Before(deadline) {
		state, found, loadErr := store.Load(context.Background())
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if found && state.SegmentID == target.ID && state.SegmentSeq == target.Seq && state.Offset == wantOffset {
			saved = state
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if saved.SegmentID == "" {
		t.Fatal("listening process did not persist the target segment and offset before the deadline")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill listening process: %v", err)
	}
	_ = command.Wait()
	childRunning = false

	state, found, err := store.Load(context.Background())
	if err != nil || !found || state.SegmentID != target.ID || state.Offset != wantOffset {
		t.Fatalf("checkpoint after forced exit found=%v state=%#v err=%v", found, state, err)
	}

	player := &resumeTestPlayer{targetSeq: target.Seq, offset: wantOffset}
	synthCalls := 0
	deps := runtimeDeps{
		startSynth: func(context.Context, kokoro.Options) (speech.Synthesizer, error) {
			synthCalls++
			return nil, errors.New("speech runtime should not start when the cache is complete")
		},
		newPlayer:           func(playback.MPVOptions) playback.Player { return player },
		startupBuffer:       time.Nanosecond,
		sessionPollInterval: 5 * time.Millisecond,
	}
	if err := runWithRuntime(context.Background(), []string{sourcePath}, nil, io.Discard, io.Discard, deps); err != nil {
		t.Fatalf("restart app from checkpoint: %v", err)
	}
	if synthCalls != 0 {
		t.Fatalf("restart launched speech runtime %d times despite cached audio", synthCalls)
	}
	seq, offset := player.firstAppend()
	if seq != target.Seq || offset != wantOffset {
		t.Fatalf("restarted playback began at sequence %d offset %s, want sequence %d offset %s", seq, offset, target.Seq, wantOffset)
	}
}

type resumeTestSynth struct{}

func (resumeTestSynth) ID() string { return kokoro.EngineID }

func (resumeTestSynth) Synthesize(ctx context.Context, request speech.Request) (speech.Result, error) {
	if err := ctx.Err(); err != nil {
		return speech.Result{}, err
	}
	if err := writeResumeWAV(request.Output); err != nil {
		return speech.Result{}, err
	}
	return speech.Result{SegmentID: request.Segment.ID, Seq: request.Segment.Seq, Path: request.Output}, nil
}

func (resumeTestSynth) Close() error { return nil }

type resumeTestPlayer struct {
	mu        sync.Mutex
	items     []playback.Item
	firstSeq  uint64
	firstOff  time.Duration
	hasFirst  bool
	targetSeq uint64
	offset    time.Duration
	blockWait bool
}

func (p *resumeTestPlayer) Start(context.Context) error { return nil }

func (p *resumeTestPlayer) Append(ctx context.Context, item playback.Item) error {
	return p.AppendAt(ctx, item, 0)
}

func (p *resumeTestPlayer) AppendAt(ctx context.Context, item playback.Item, offset time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.hasFirst {
		p.firstSeq = item.Seq
		p.firstOff = offset
		p.hasFirst = true
	}
	p.items = append(p.items, item)
	return nil
}

func (p *resumeTestPlayer) Position(context.Context) (uint64, time.Duration, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, item := range p.items {
		if item.Seq == p.targetSeq {
			return p.targetSeq, p.offset, nil
		}
	}
	if len(p.items) == 0 {
		return 0, 0, errors.New("no audio has been queued")
	}
	return p.items[0].Seq, 0, nil
}

func (p *resumeTestPlayer) Wait(context.Context) error {
	if p.blockWait {
		select {}
	}
	return nil
}

func (p *resumeTestPlayer) Close() error { return nil }

func (p *resumeTestPlayer) firstAppend() (uint64, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.firstSeq, p.firstOff
}

func writeResumeWAV(path string) error {
	const sampleRate = uint32(24000)
	const frames = sampleRate * 3
	dataBytes := frames * 2
	var header [44]byte
	copy(header[:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataBytes)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], sampleRate*2)
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
	return os.WriteFile(path, append(header[:], make([]byte, dataBytes)...), 0o600)
}
