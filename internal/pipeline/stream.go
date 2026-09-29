package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/playback"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/translate"
)

type SpeechCache interface {
	PrepareOutput(key string) (string, error)
	Lookup(context.Context, string) (speech.Result, bool, error)
	Store(context.Context, string, speech.Result) error
}

type StreamOptions struct {
	Workers               int
	QueueCapacity         int
	StartupBuffer         time.Duration
	BufferHealthInterval  time.Duration
	Voice                 string
	Speed                 float64
	ResumeOffset          time.Duration
	Translator            translate.Translator
	TranslationCache      *translate.Cache
	TranslationContext    translate.ContextArtifact
	TargetLanguage        string
	TranslationWorkers    int
	TranslationBatchSize  int
	OnProgress            func(completed, total int)
	OnPlaybackStart       func()
	OnTranslationMeasured func(time.Duration, []segment.Segment)
	OnSpeechMeasured      func(segment.Segment, time.Duration, time.Duration)
	OnBufferHealth        func(float64, time.Time)
}

const defaultBufferHealthInterval = time.Second

type StreamReport struct {
	Generated            int
	Reused               int
	Appended             int
	TranslationGenerated int
	TranslationReused    int
	Results              []speech.Result
}

type synthesis struct {
	result speech.Result
	cached bool
	err    error
}

type orderedSpeech struct {
	result speech.Result
	cached bool
}

// Stream synthesizes segments concurrently, commits only contiguous sequence
// numbers, then feeds the player after enough ordered audio has accumulated.
func Stream(ctx context.Context, plan segment.Plan, synthesizer speech.Synthesizer, audioCache SpeechCache, player playback.Player, options StreamOptions) (StreamReport, error) {
	var report StreamReport
	if ctx == nil {
		return report, errors.New("stream requires a context")
	}
	if synthesizer == nil {
		return report, errors.New("stream requires a synthesizer")
	}
	if len(plan.Segments) == 0 {
		return report, errors.New("there is no readable audio to play")
	}
	options = normalizeStreamOptions(options)
	if options.Translator != nil {
		if err := validateTranslationOptions(ctx, plan, &options); err != nil {
			return report, err
		}
	}
	resumeOffset := options.ResumeOffset
	firstSeq := plan.Segments[0].Seq
	for index, item := range plan.Segments {
		if item.Seq != firstSeq+uint64(index) {
			return report, fmt.Errorf("segment plan is not contiguous at sequence %d", item.Seq)
		}
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	streamSucceeded := false
	defer func() {
		if !streamSucceeded && player != nil {
			_ = player.Close()
		}
	}()
	jobs := make(chan segment.Segment, options.QueueCapacity)
	completed := make(chan synthesis, options.QueueCapacity)
	producerDone := make(chan struct{})
	var translationStats translationCounters
	var workers sync.WaitGroup
	for i := 0; i < options.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				outcome := synthesize(workCtx, item, synthesizer, audioCache, options)
				select {
				case completed <- outcome:
				case <-workCtx.Done():
					return
				}
			}
		}()
	}
	if options.Translator == nil {
		go func() {
			defer close(producerDone)
			defer close(jobs)
			for _, item := range plan.Segments {
				select {
				case jobs <- item:
				case <-workCtx.Done():
					return
				}
			}
		}()
	} else {
		go func() {
			defer close(producerDone)
			translateToSpeechJobs(workCtx, cancel, plan.Segments, jobs, completed, options, &translationStats)
		}()
	}
	go func() {
		workers.Wait()
		<-producerDone
		close(completed)
	}()

	reorder := NewReorderBuffer[orderedSpeech](firstSeq)
	var firstErr error
	var buffered []speech.Result
	var bufferedDuration time.Duration
	started := false
	var playbackStartedAt time.Time
	var queuedItems []playback.Item
	var queuedDuration time.Duration
	initialResumeOffset := resumeOffset
	var healthTicker *time.Ticker
	var healthTicks <-chan time.Time
	if player != nil && options.OnBufferHealth != nil {
		healthTicker = time.NewTicker(options.BufferHealthInterval)
		healthTicks = healthTicker.C
		defer healthTicker.Stop()
	}
	for completedCh := (<-chan synthesis)(completed); completedCh != nil; {
		select {
		case <-healthTicks:
			if started {
				observeBufferHealth(workCtx, player, queuedItems, queuedDuration, initialResumeOffset, playbackStartedAt, options.OnBufferHealth)
			}
		case outcome, ok := <-completedCh:
			if !ok {
				completedCh = nil
				continue
			}
			if outcome.err != nil {
				if firstErr == nil && !errors.Is(outcome.err, context.Canceled) && !errors.Is(outcome.err, context.DeadlineExceeded) {
					firstErr = outcome.err
					cancel()
				}
				continue
			}
			if firstErr != nil {
				continue
			}
			ready, err := reorder.Push(outcome.result.Seq, orderedSpeech{result: outcome.result, cached: outcome.cached})
			if err != nil {
				firstErr = err
				cancel()
				continue
			}
			for _, ordered := range ready {
				result := ordered.result
				report.Results = append(report.Results, result)
				if options.OnProgress != nil {
					options.OnProgress(report.Generated+report.Reused+1, len(plan.Segments))
				}
				if ordered.cached {
					report.Reused++
				} else {
					report.Generated++
				}
				if player == nil {
					continue
				}
				if started {
					item := playback.Item{Seq: result.Seq, Path: result.Path, Duration: result.Duration}
					if err := player.Append(workCtx, item); err != nil {
						firstErr = fmt.Errorf("append audio to player: %w", err)
						cancel()
						break
					}
					report.Appended++
					queuedItems = append(queuedItems, item)
					queuedDuration += result.Duration
					observeBufferHealth(workCtx, player, queuedItems, queuedDuration, initialResumeOffset, playbackStartedAt, options.OnBufferHealth)
					continue
				}
				buffered = append(buffered, result)
				bufferedDuration += result.Duration
				if bufferedDuration >= options.StartupBuffer {
					if err := startAndAppend(ctx, workCtx, player, buffered, &report, options.OnPlaybackStart, resumeOffset); err != nil {
						firstErr = err
						cancel()
						break
					}
					started = true
					playbackStartedAt = time.Now()
					for _, queued := range buffered {
						queuedItems = append(queuedItems, playback.Item{Seq: queued.Seq, Path: queued.Path, Duration: queued.Duration})
					}
					queuedDuration = bufferedDuration - initialResumeOffset
					if queuedDuration < 0 {
						queuedDuration = 0
					}
					observeBufferHealth(workCtx, player, queuedItems, queuedDuration, initialResumeOffset, playbackStartedAt, options.OnBufferHealth)
					buffered = nil
					resumeOffset = 0
				}
			}
		}
	}
	if firstErr != nil {
		return report, firstErr
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if reorder.Next() != firstSeq+uint64(len(plan.Segments)) {
		return report, fmt.Errorf("audio generation ended before sequence %d was committed", reorder.Next())
	}
	if player != nil && !started {
		if err := startAndAppend(ctx, ctx, player, buffered, &report, options.OnPlaybackStart, resumeOffset); err != nil {
			return report, err
		}
		playbackStartedAt = time.Now()
		for _, queued := range buffered {
			queuedItems = append(queuedItems, playback.Item{Seq: queued.Seq, Path: queued.Path, Duration: queued.Duration})
		}
		queuedDuration = bufferedDuration - initialResumeOffset
		if queuedDuration < 0 {
			queuedDuration = 0
		}
		observeBufferHealth(ctx, player, queuedItems, queuedDuration, initialResumeOffset, playbackStartedAt, options.OnBufferHealth)
	}
	report.TranslationGenerated = translationStats.generated
	report.TranslationReused = translationStats.reused
	streamSucceeded = true
	return report, nil
}

func normalizeStreamOptions(options StreamOptions) StreamOptions {
	if options.Workers <= 0 {
		options.Workers = 1
	}
	if options.QueueCapacity <= 0 {
		options.QueueCapacity = options.Workers * 2
	}
	if options.StartupBuffer <= 0 {
		options.StartupBuffer = 20 * time.Second
	}
	if options.BufferHealthInterval <= 0 {
		options.BufferHealthInterval = defaultBufferHealthInterval
	}
	if options.Speed <= 0 {
		options.Speed = 1
	}
	if options.TranslationWorkers <= 0 {
		options.TranslationWorkers = options.Workers
	}
	if options.TranslationBatchSize <= 0 {
		options.TranslationBatchSize = 4
	}
	if options.TranslationBatchSize > 16 {
		options.TranslationBatchSize = 16
	}
	if options.Voice == "" {
		options.Voice = "af_heart"
	}
	return options
}

func synthesize(ctx context.Context, item segment.Segment, synthesizer speech.Synthesizer, audioCache SpeechCache, options StreamOptions) synthesis {
	request := speech.Request{Segment: item, Voice: options.Voice, Speed: options.Speed}
	key := ""
	if audioCache != nil {
		var err error
		key, err = speech.CacheKey(synthesizer.ID(), request)
		if err != nil {
			return synthesis{err: err}
		}
		cached, found, err := audioCache.Lookup(ctx, key)
		if err != nil {
			return synthesis{err: fmt.Errorf("read speech cache: %w", err)}
		}
		if found {
			cached.SegmentID = item.ID
			cached.Seq = item.Seq
			return synthesis{result: cached, cached: true}
		}
		request.Output, err = audioCache.PrepareOutput(key)
		if err != nil {
			return synthesis{err: fmt.Errorf("prepare speech cache: %w", err)}
		}
	}
	started := time.Now()
	result, err := synthesizer.Synthesize(ctx, request)
	wallTime := time.Since(started)
	if err != nil {
		return synthesis{err: fmt.Errorf("synthesize segment %d: %w", item.Seq, err)}
	}
	if result.SegmentID != item.ID || result.Seq != item.Seq || result.Path == "" {
		return synthesis{err: fmt.Errorf("synthesizer returned an invalid result for segment %d", item.Seq)}
	}
	if options.OnSpeechMeasured != nil {
		options.OnSpeechMeasured(item, wallTime, result.Duration)
	}
	if audioCache != nil {
		if err := audioCache.Store(ctx, key, result); err != nil {
			return synthesis{err: fmt.Errorf("store speech cache: %w", err)}
		}
	}
	return synthesis{result: result}
}

func startAndAppend(startCtx, appendCtx context.Context, player playback.Player, items []speech.Result, report *StreamReport, onPlaybackStart func(), resumeOffset time.Duration) error {
	if len(items) == 0 {
		return errors.New("no ordered audio was ready for playback")
	}
	if err := player.Start(startCtx); err != nil {
		return fmt.Errorf("start playback: %w", err)
	}
	if onPlaybackStart != nil {
		onPlaybackStart()
	}
	for _, result := range items {
		item := playback.Item{Seq: result.Seq, Path: result.Path, Duration: result.Duration}
		var err error
		if report.Appended == 0 && resumeOffset > 0 {
			if appender, ok := player.(playback.OffsetAppender); ok {
				err = appender.AppendAt(appendCtx, item, resumeOffset)
			} else {
				err = player.Append(appendCtx, item)
			}
		} else {
			err = player.Append(appendCtx, item)
		}
		if err != nil {
			return fmt.Errorf("append audio to player: %w", err)
		}
		report.Appended++
	}
	if resumeOffset > 0 {
		if _, ok := player.(playback.OffsetAppender); ok {
			return nil
		}
		seeker, ok := player.(playback.Seeker)
		if !ok {
			return errors.New("player cannot resume within an audio segment")
		}
		if err := seeker.Seek(appendCtx, resumeOffset); err != nil {
			return fmt.Errorf("resume playback position: %w", err)
		}
	}
	return nil
}

func observeBufferHealth(
	ctx context.Context,
	player playback.Player,
	items []playback.Item,
	queuedDuration, resumeOffset time.Duration,
	startedAt time.Time,
	callback func(float64, time.Time),
) {
	if callback == nil || player == nil || len(items) == 0 {
		return
	}
	seconds := (queuedDuration - time.Since(startedAt)).Seconds()
	if seconds < 0 {
		seconds = 0
	}
	positionCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	seq, offset, err := player.Position(positionCtx)
	cancel()
	if err == nil {
		var consumed time.Duration
		for index, item := range items {
			itemDuration := item.Duration
			if index == 0 {
				itemDuration -= resumeOffset
				if itemDuration < 0 {
					itemDuration = 0
				}
			}
			if item.Seq == seq {
				if index == 0 {
					offset -= resumeOffset
				}
				if offset < 0 {
					offset = 0
				}
				if offset > item.Duration {
					offset = item.Duration
				}
				consumed += offset
				remaining := (queuedDuration - consumed).Seconds()
				if remaining < 0 {
					remaining = 0
				}
				seconds = remaining
				break
			}
			consumed += itemDuration
		}
	}
	callback(seconds, time.Now())
}
