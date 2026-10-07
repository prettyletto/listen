package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/testkit"
)

func TestStreamCommitsDelayedSynthesisInSourceOrder(t *testing.T) {
	synth := &testkit.FakeSynthesizer{
		DelayBySeq: map[uint64]time.Duration{0: 60 * time.Millisecond, 1: 20 * time.Millisecond, 2: time.Millisecond},
		Duration:   time.Second,
	}
	player := &recordingPlayer{}
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 0, ID: "a"}, {Seq: 1, ID: "b"}, {Seq: 2, ID: "c"}}}
	report, err := Stream(context.Background(), plan, synth, nil, player, StreamOptions{
		Workers: 3, QueueCapacity: 3, StartupBuffer: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !player.started {
		t.Fatal("player did not start after the initial buffer")
	}
	if len(player.items) != 3 || player.items[0].Seq != 0 || player.items[1].Seq != 1 || player.items[2].Seq != 2 {
		t.Fatalf("playback order was not deterministic: %#v", player.items)
	}
	if report.Generated != 3 || report.Reused != 0 || report.Appended != 3 {
		t.Fatalf("unexpected stream report: %#v", report)
	}
}

func TestStreamReportsSynthesisStartBeforeSegmentCompletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan segment.Segment, 1)
	done := make(chan error, 1)
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 0, ID: "opening", SpokenText: "Opening passage."}}}
	synth := &testkit.FakeSynthesizer{DelayBySeq: map[uint64]time.Duration{0: time.Hour}}
	go func() {
		_, err := Stream(ctx, plan, synth, nil, nil, StreamOptions{
			OnSynthesisStarted: func(item segment.Segment) { started <- item },
		})
		done <- err
	}()

	select {
	case item := <-started:
		if item.ID != "opening" || item.Seq != 0 {
			t.Fatalf("synthesis start reported %#v, want the opening segment", item)
		}
	case <-time.After(time.Second):
		t.Fatal("synthesis start was not reported before the delayed segment finished")
	}
	select {
	case err := <-done:
		t.Fatalf("stream completed before synthesis ended: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled stream returned %v, want context.Canceled", err)
	}
}

func TestStreamStartsPlaybackBeforeRemainingSegmentsFinish(t *testing.T) {
	synth := &testkit.FakeSynthesizer{
		DelayBySeq: map[uint64]time.Duration{0: 5 * time.Millisecond, 1: 10 * time.Millisecond, 2: 400 * time.Millisecond},
		Duration:   time.Second,
	}
	player := &recordingPlayer{startedCh: make(chan struct{})}
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 0, ID: "a"}, {Seq: 1, ID: "b"}, {Seq: 2, ID: "c"}}}
	done := make(chan error, 1)
	go func() {
		_, err := Stream(context.Background(), plan, synth, nil, player, StreamOptions{
			Workers: 3, QueueCapacity: 3, StartupBuffer: 2 * time.Second,
		})
		done <- err
	}()
	select {
	case <-player.startedCh:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("playback waited for the final delayed segment")
	}
	if count := len(synth.Snapshot()); count >= len(plan.Segments) {
		t.Fatalf("playback started after all synthesis finished: generated=%d", count)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStreamFromStartsAudioWhileSegmentProducerIsStillRunning(t *testing.T) {
	player := &recordingPlayer{startedCh: make(chan struct{})}
	firstPagesReady := make(chan struct{})
	finishProducer := make(chan struct{})
	producerFinished := make(chan struct{})
	producer := func(ctx context.Context, emit func(segment.Segment) error, boundary func() error) error {
		if err := emit(segment.Segment{ID: "page-0", Seq: 0, SpokenText: "opening page"}); err != nil {
			return err
		}
		if err := boundary(); err != nil {
			return err
		}
		close(firstPagesReady)
		select {
		case <-finishProducer:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := emit(segment.Segment{ID: "page-1", Seq: 1, SpokenText: "later page"})
		close(producerFinished)
		return err
	}
	done := make(chan struct {
		report StreamReport
		err    error
	}, 1)
	go func() {
		report, err := StreamFrom(context.Background(), 0, producer, &testkit.FakeSynthesizer{Duration: 100 * time.Millisecond}, nil, player, StreamOptions{
			Workers: 1, QueueCapacity: 1, StartupBuffer: 3 * time.Second,
		})
		done <- struct {
			report StreamReport
			err    error
		}{report: report, err: err}
	}()
	select {
	case <-firstPagesReady:
	case <-time.After(time.Second):
		t.Fatal("producer did not release the opening segments")
	}
	select {
	case <-player.startedCh:
	case <-time.After(time.Second):
		t.Fatal("playback waited for the producer to finish the document")
	}
	select {
	case <-producerFinished:
		t.Fatal("producer finished before playback started")
	default:
	}
	close(finishProducer)
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.report.Generated != 2 || len(player.items) != 2 {
			t.Fatalf("stream report=%#v queued=%#v, want two ordered segments", result.report, player.items)
		}
		for index, item := range player.items {
			if item.Seq != uint64(index) {
				t.Fatalf("player received segment sequence %d at index %d", item.Seq, index)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live segment stream did not finish")
	}
}

func TestStreamFromRejectsNonContiguousSegments(t *testing.T) {
	producer := func(_ context.Context, emit func(segment.Segment) error, _ func() error) error {
		return emit(segment.Segment{ID: "gap", Seq: 1})
	}
	_, err := StreamFrom(context.Background(), 0, producer, &testkit.FakeSynthesizer{Duration: time.Second}, nil, nil, StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "not contiguous at sequence 0") {
		t.Fatalf("non-contiguous producer error = %v", err)
	}
}

func TestStreamLeavesPlayerAliveUntilCallerWaits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	player := &lifetimePlayer{}
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 0, ID: "only"}}}
	if _, err := Stream(ctx, plan, &testkit.FakeSynthesizer{Duration: time.Second}, nil, player, StreamOptions{StartupBuffer: time.Second}); err != nil {
		t.Fatal(err)
	}
	if player.startContext == nil || player.startContext.Err() != nil {
		t.Fatalf("player lifetime was tied to the finished synthesis context: %v", player.startContext)
	}
	if player.closeCalls != 0 {
		t.Fatalf("successful stream closed its player %d time(s) before the caller could wait", player.closeCalls)
	}
	cancel()
	if !errors.Is(player.startContext.Err(), context.Canceled) {
		t.Fatalf("player lifetime did not follow the caller context: %v", player.startContext.Err())
	}
}

func TestStreamReusesCompletedSpeechCache(t *testing.T) {
	synth := &testkit.FakeSynthesizer{Duration: time.Second}
	cache := &memorySpeechCache{results: make(map[string]speech.Result)}
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 0, ID: "a", SpokenText: "one"}, {Seq: 1, ID: "b", SpokenText: "two"}}}
	first := &recordingPlayer{}
	if _, err := Stream(context.Background(), plan, synth, cache, first, StreamOptions{StartupBuffer: time.Second}); err != nil {
		t.Fatal(err)
	}
	second := &recordingPlayer{}
	report, err := Stream(context.Background(), plan, synth, cache, second, StreamOptions{StartupBuffer: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if report.Reused != 2 || report.Generated != 0 {
		t.Fatalf("cached rerun did not reuse audio: %#v", report)
	}
	if len(report.Results) != 2 || report.Results[0].SegmentID != "a" || report.Results[1].SegmentID != "b" {
		t.Fatalf("ordered pipeline audio results = %#v", report.Results)
	}
	if len(synth.Snapshot()) != 2 {
		t.Fatalf("cache miss generated extra audio: %#v", synth.Snapshot())
	}
}

func TestStreamResumesAtRequestedSequenceAndOffset(t *testing.T) {
	player := &seekingPlayer{recordingPlayer: &recordingPlayer{}}
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 7, ID: "resume"}, {Seq: 8, ID: "next"}}}
	_, err := Stream(context.Background(), plan, &testkit.FakeSynthesizer{Duration: time.Second}, nil, player, StreamOptions{
		StartupBuffer: time.Second, ResumeOffset: 2500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !player.started || len(player.items) != 2 || player.items[0].Seq != 7 || player.offset != 2500*time.Millisecond {
		t.Fatalf("resumed playback = started:%v items:%#v offset:%s", player.started, player.items, player.offset)
	}
}

func TestStreamAppliesResumeOffsetDuringFirstAppendWhenSupported(t *testing.T) {
	player := &offsetAppendingPlayer{recordingPlayer: &recordingPlayer{}}
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 7, ID: "resume"}, {Seq: 8, ID: "next"}}}
	_, err := Stream(context.Background(), plan, &testkit.FakeSynthesizer{Duration: time.Second}, nil, player, StreamOptions{
		StartupBuffer: time.Second, ResumeOffset: 2500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !player.started || len(player.items) != 2 || player.items[0].Seq != 7 || player.offset != 2500*time.Millisecond {
		t.Fatalf("resumed playback = started:%v items:%#v offset:%s", player.started, player.items, player.offset)
	}
	if player.seekCalls != 0 {
		t.Fatalf("resume issued %d separate seek calls after starting the first file", player.seekCalls)
	}
}

func TestStreamCancellationStopsWorkersAndDoesNotStartPlayer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	synth := &testkit.FakeSynthesizer{DelayBySeq: map[uint64]time.Duration{0: time.Minute}, Duration: time.Second}
	player := &recordingPlayer{}
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 0, ID: "a"}, {Seq: 1, ID: "b"}}}
	done := make(chan error, 1)
	go func() {
		_, err := Stream(ctx, plan, synth, nil, player, StreamOptions{Workers: 2})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
	if player.started {
		t.Fatal("player started without the configured buffer")
	}
}

func TestStreamSamplesBufferDuringProducerStall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	player := &advancingPositionPlayer{}
	bufferSamples := make(chan float64, 16)
	plan := segment.Plan{Segments: []segment.Segment{{Seq: 0, ID: "ready"}, {Seq: 1, ID: "stalled"}}}
	done := make(chan error, 1)
	go func() {
		_, err := Stream(ctx, plan, &testkit.FakeSynthesizer{
			DelayBySeq: map[uint64]time.Duration{1: time.Minute}, Duration: 50 * time.Second,
		}, nil, player, StreamOptions{
			Workers: 1, QueueCapacity: 2, StartupBuffer: 20 * time.Second,
			BufferHealthInterval: 5 * time.Millisecond,
			OnBufferHealth:       func(seconds float64, _ time.Time) { bufferSamples <- seconds },
		})
		done <- err
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	warnThresholdReached := false
	for !warnThresholdReached {
		select {
		case seconds := <-bufferSamples:
			warnThresholdReached = seconds <= 45
		case <-deadline.C:
			t.Fatal("periodic buffer sampling did not detect the 45-second warning threshold during the producer stall")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream after cancelling stalled synthesis = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancelling stalled synthesis")
	}
	if got := player.appendCount(); got != 1 {
		t.Fatalf("producer stall appended %d audio items before cancellation, want only the ready item", got)
	}
}

type recordingPlayer struct {
	started   bool
	items     []playback.Item
	mu        sync.Mutex
	startedCh chan struct{}
}

type lifetimePlayer struct {
	startContext context.Context
	closeCalls   int
}

func (p *lifetimePlayer) Start(ctx context.Context) error {
	p.startContext = ctx
	return nil
}

func (*lifetimePlayer) Append(context.Context, playback.Item) error { return nil }
func (*lifetimePlayer) Position(context.Context) (uint64, time.Duration, error) {
	return 0, 0, nil
}
func (*lifetimePlayer) Wait(context.Context) error { return nil }
func (p *lifetimePlayer) Close() error {
	p.closeCalls++
	return nil
}

// advancingPositionPlayer reports a controlled playback advance on each
// position query so buffer-warning tests do not need to wait through long
// audio durations.
type advancingPositionPlayer struct {
	mu        sync.Mutex
	started   bool
	items     []playback.Item
	positions int
}

func (p *advancingPositionPlayer) Start(context.Context) error {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	return nil
}

func (p *advancingPositionPlayer) Append(_ context.Context, item playback.Item) error {
	p.mu.Lock()
	p.items = append(p.items, item)
	p.mu.Unlock()
	return nil
}

func (p *advancingPositionPlayer) Position(ctx context.Context) (uint64, time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.positions++
	if p.positions > 1 {
		return 0, 10 * time.Second, nil
	}
	return 0, 0, nil
}

func (p *advancingPositionPlayer) Wait(context.Context) error { return nil }

func (p *advancingPositionPlayer) Close() error { return nil }

func (p *advancingPositionPlayer) appendCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.items)
}

type seekingPlayer struct {
	*recordingPlayer
	offset time.Duration
}

type offsetAppendingPlayer struct {
	*recordingPlayer
	offset    time.Duration
	seekCalls int
}

func (p *offsetAppendingPlayer) AppendAt(ctx context.Context, item playback.Item, offset time.Duration) error {
	p.offset = offset
	return p.recordingPlayer.Append(ctx, item)
}

func (p *offsetAppendingPlayer) Seek(_ context.Context, offset time.Duration) error {
	p.seekCalls++
	p.offset = offset
	return nil
}

func (p *seekingPlayer) Seek(_ context.Context, offset time.Duration) error {
	p.offset = offset
	return nil
}

func (p *recordingPlayer) Start(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = true
	if p.startedCh != nil {
		close(p.startedCh)
	}
	return nil
}

func (p *recordingPlayer) Append(_ context.Context, item playback.Item) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		return errors.New("append before start")
	}
	p.items = append(p.items, item)
	return nil
}

func (p *recordingPlayer) Position(context.Context) (uint64, time.Duration, error) {
	return 0, 0, nil
}

func (p *recordingPlayer) Wait(context.Context) error { return nil }

func (p *recordingPlayer) Close() error { return nil }

type memorySpeechCache struct {
	mu      sync.Mutex
	results map[string]speech.Result
}

func (c *memorySpeechCache) PrepareOutput(key string) (string, error) {
	return "fake://speech/" + key, nil
}

func (c *memorySpeechCache) Lookup(_ context.Context, key string) (speech.Result, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, ok := c.results[key]
	return result, ok, nil
}

func (c *memorySpeechCache) Store(_ context.Context, key string, result speech.Result) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[key] = result
	return nil
}
