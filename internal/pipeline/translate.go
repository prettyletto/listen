package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/translate"
)

type translationCounters struct {
	generated int
	reused    int
}

type TranslationProbe struct {
	WallTime   time.Duration
	Translated []segment.Segment
}

// ProbeTranslation translates a small sample from the opening major unit so
// playback can be classified before synthesis and buffer startup. Results are
// committed through the same translation cache used by the streaming path.
func ProbeTranslation(
	ctx context.Context,
	items []segment.Segment,
	translator translate.Translator,
	cache *translate.Cache,
	artifact translate.ContextArtifact,
	maxSegments int,
) (TranslationProbe, error) {
	if ctx == nil {
		return TranslationProbe{}, errors.New("translation probe requires a context")
	}
	if translator == nil {
		return TranslationProbe{}, errors.New("translation probe requires a translator")
	}
	if len(items) == 0 {
		return TranslationProbe{}, errors.New("translation probe requires at least one segment")
	}
	if maxSegments <= 0 || maxSegments > 16 {
		maxSegments = 4
	}
	count := 1
	for count < len(items) && count < maxSegments {
		if items[count].MajorUnitID != items[0].MajorUnitID || items[count].SourceLang != items[0].SourceLang {
			break
		}
		count++
	}
	started := time.Now()
	values, err := translateBatch(ctx, items[:count], translator, cache, artifact)
	if err != nil {
		return TranslationProbe{}, fmt.Errorf("preflight local translation: %w", err)
	}
	probe := TranslationProbe{WallTime: time.Since(started)}
	for _, value := range values {
		if !value.cached {
			probe.Translated = append(probe.Translated, value.segment)
		}
	}
	return probe, nil
}

type translationValue struct {
	segment segment.Segment
	cached  bool
}

type translatedBatch struct {
	values []translationValue
	err    error
}

func validateTranslationOptions(ctx context.Context, plan segment.Plan, options *StreamOptions) error {
	if options == nil || options.Translator == nil {
		return nil
	}
	if options.TargetLanguage == "" {
		options.TargetLanguage = options.TranslationContext.TargetLanguage
	}
	target, err := translate.NormalizeLanguageTag(options.TargetLanguage)
	if err != nil {
		return err
	}
	options.TargetLanguage = target
	if options.TranslationContext.DocumentID != plan.DocumentID {
		return errors.New("translation context belongs to a different document")
	}
	contextTarget, err := translate.NormalizeLanguageTag(options.TranslationContext.TargetLanguage)
	if err != nil {
		return err
	}
	if contextTarget != target {
		return errors.New("translation context target does not match requested language")
	}
	if strings.TrimSpace(options.Translator.ID()) == "" {
		return errors.New("translation backend ID is empty")
	}
	capabilities, err := options.Translator.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("read translation backend capabilities: %w", err)
	}
	if capabilities.Remote {
		if !translate.IsCloudAuthorized(options.Translator) {
			return errors.New("remote translation requires explicit cloud authorization")
		}
	} else if !capabilities.Local {
		return errors.New("translation backend is neither local nor explicitly authorized for cloud use")
	}
	for _, item := range plan.Segments {
		sourceLanguage := item.SourceLang
		if strings.TrimSpace(sourceLanguage) == "" {
			sourceLanguage = options.TranslationContext.SourceLanguage
		}
		if !capabilities.SupportsLanguagePair(sourceLanguage, target) {
			pair, pairErr := translate.LanguagePair(sourceLanguage, target)
			if pairErr != nil {
				pair = "the declared source language to " + target
			}
			return fmt.Errorf("translation backend %q does not support %s", capabilities.Name, pair)
		}
	}
	if !capabilities.SupportsBatch {
		options.TranslationBatchSize = 1
	}
	if _, err := options.TranslationContext.Fingerprint(); err != nil {
		return err
	}
	return nil
}

func translateToSpeechJobs(
	ctx context.Context,
	cancel context.CancelFunc,
	items []segment.Segment,
	speechJobs chan<- segment.Segment,
	errorsOut chan<- synthesis,
	options StreamOptions,
	counters *translationCounters,
) {
	defer close(speechJobs)
	if len(items) == 0 {
		return
	}
	batchCtx, cancelBatches := context.WithCancel(ctx)
	defer cancelBatches()
	batches := make(chan []segment.Segment, options.QueueCapacity)
	results := make(chan translatedBatch, options.QueueCapacity)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer close(batches)
		var current []segment.Segment
		flush := func() bool {
			if len(current) == 0 {
				return true
			}
			batch := append([]segment.Segment(nil), current...)
			current = current[:0]
			select {
			case batches <- batch:
				return true
			case <-batchCtx.Done():
				return false
			}
		}
		for _, item := range items {
			if len(current) > 0 && (len(current) >= options.TranslationBatchSize || current[0].MajorUnitID != item.MajorUnitID || current[0].SourceLang != item.SourceLang) {
				if !flush() {
					return
				}
			}
			current = append(current, item)
		}
		flush()
	}()

	var workers sync.WaitGroup
	var reportOnce sync.Once
	for index := 0; index < options.TranslationWorkers; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for batch := range batches {
				started := time.Now()
				values, err := translateBatch(batchCtx, batch, options.Translator, options.TranslationCache, options.TranslationContext)
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return
					}
					reportOnce.Do(func() {
						select {
						case results <- translatedBatch{err: fmt.Errorf("translate segment batch beginning at %d: %w", batch[0].Seq, err)}:
						case <-ctx.Done():
						}
						cancelBatches()
					})
					return
				}
				if options.OnTranslationMeasured != nil {
					measured := make([]segment.Segment, 0, len(values))
					for _, value := range values {
						if !value.cached {
							measured = append(measured, value.segment)
						}
					}
					if len(measured) > 0 {
						options.OnTranslationMeasured(time.Since(started), measured)
					}
				}
				select {
				case results <- translatedBatch{values: values}:
				case <-batchCtx.Done():
					return
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		<-producerDone
		close(results)
	}()

	firstSeq := items[0].Seq
	expectedNext := firstSeq + uint64(len(items))
	reorder := NewReorderBuffer[translationValue](firstSeq)
	var firstErr error
	for result := range results {
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancelBatches()
			}
			continue
		}
		if firstErr != nil {
			continue
		}
		for _, value := range result.values {
			ready, err := reorder.Push(value.segment.Seq, value)
			if err != nil {
				firstErr = err
				cancelBatches()
				break
			}
			for _, ordered := range ready {
				if ordered.cached {
					counters.reused++
				} else {
					counters.generated++
				}
				select {
				case speechJobs <- ordered.segment:
				case <-ctx.Done():
					return
				}
			}
		}
	}
	if firstErr == nil && ctx.Err() == nil && reorder.Next() != expectedNext {
		firstErr = fmt.Errorf("translation ended before sequence %d was committed", reorder.Next())
	}
	if firstErr != nil && ctx.Err() == nil {
		select {
		case errorsOut <- synthesis{err: firstErr}:
		case <-ctx.Done():
		}
		cancel()
	}
}

func translateBatch(
	ctx context.Context,
	items []segment.Segment,
	translator translate.Translator,
	cache *translate.Cache,
	artifact translate.ContextArtifact,
) ([]translationValue, error) {
	if len(items) == 0 {
		return nil, errors.New("translation batch is empty")
	}
	texts := make(map[string]string, len(items))
	cached := make(map[string]bool, len(items))
	keys := make(map[string]string, len(items))
	request := translate.Request{
		TargetLanguage:   artifact.TargetLanguage,
		Glossary:         artifact.Glossary,
		ContextHint:      artifact.Summary,
		StyleInstruction: artifact.StyleInstruction,
		PromptVersion:    artifact.PromptVersion,
		Segments:         make([]segment.Segment, 0, len(items)),
	}
	for _, item := range items {
		if item.ID == "" {
			return nil, errors.New("translation segment ID is empty")
		}
		if _, duplicate := keys[item.ID]; duplicate {
			return nil, fmt.Errorf("translation batch repeats segment ID %q", item.ID)
		}
		key, err := translate.CacheKey(translator.ID(), item, artifact)
		if err != nil {
			return nil, err
		}
		keys[item.ID] = key
		if cache != nil {
			result, found, err := cache.Lookup(ctx, key)
			if err != nil {
				return nil, fmt.Errorf("read translation cache: %w", err)
			}
			if found {
				texts[item.ID] = result.Text
				cached[item.ID] = true
				continue
			}
		}
		item.TargetLang = artifact.TargetLanguage
		request.Segments = append(request.Segments, item)
	}
	if len(request.Segments) > 0 {
		translated, err := translator.Translate(ctx, request)
		if err != nil {
			return nil, err
		}
		requested := make(map[string]bool, len(request.Segments))
		for _, item := range request.Segments {
			requested[item.ID] = true
		}
		for _, result := range translated {
			if !requested[result.SegmentID] {
				return nil, fmt.Errorf("translator returned an unknown segment ID %q", result.SegmentID)
			}
			if _, exists := texts[result.SegmentID]; exists {
				return nil, fmt.Errorf("translator returned segment %q more than once", result.SegmentID)
			}
			text, err := translate.NormalizeTranslationText(result.Text)
			if err != nil {
				return nil, fmt.Errorf("translator returned invalid text for segment %q: %w", result.SegmentID, err)
			}
			texts[result.SegmentID] = text
		}
		for _, item := range request.Segments {
			text, found := texts[item.ID]
			if !found {
				return nil, fmt.Errorf("translator omitted segment %q", item.ID)
			}
			if cache != nil {
				if err := cache.Store(ctx, keys[item.ID], translate.Result{SegmentID: item.ID, Text: text}); err != nil {
					return nil, fmt.Errorf("write translation cache: %w", err)
				}
			}
		}
	}

	values := make([]translationValue, 0, len(items))
	for _, item := range items {
		text, found := texts[item.ID]
		if !found {
			return nil, fmt.Errorf("translation result missing for segment %q", item.ID)
		}
		item.SpokenText = text
		item.TargetLang = artifact.TargetLanguage
		values = append(values, translationValue{segment: item, cached: cached[item.ID]})
	}
	return values, nil
}
