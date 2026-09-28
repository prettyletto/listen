// Package testkit provides deterministic implementations for pipeline tests.
package testkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/translate"
)

type FakeSynthesizer struct {
	DelayBySeq map[uint64]time.Duration
	Duration   time.Duration
	Fail       map[uint64]error

	mu       sync.Mutex
	Results  []speech.Result
	Requests []speech.Request
}

func (f *FakeSynthesizer) ID() string { return "fake-speech" }

func (f *FakeSynthesizer) Synthesize(ctx context.Context, request speech.Request) (speech.Result, error) {
	f.mu.Lock()
	f.Requests = append(f.Requests, copySpeechRequest(request))
	f.mu.Unlock()
	if delay := f.DelayBySeq[request.Segment.Seq]; delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return speech.Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	if err := f.Fail[request.Segment.Seq]; err != nil {
		return speech.Result{}, err
	}
	duration := f.Duration
	if duration <= 0 {
		duration = time.Second
	}
	result := speech.Result{
		SegmentID: request.Segment.ID,
		Seq:       request.Segment.Seq,
		Path:      fmt.Sprintf("fake://speech/%s", request.Segment.ID),
		Duration:  duration,
	}
	f.mu.Lock()
	f.Results = append(f.Results, result)
	f.mu.Unlock()
	return result, nil
}

func (f *FakeSynthesizer) Close() error { return nil }

func (f *FakeSynthesizer) Snapshot() []speech.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]speech.Result(nil), f.Results...)
}

func (f *FakeSynthesizer) RequestSnapshot() []speech.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := make([]speech.Request, len(f.Requests))
	for index, request := range f.Requests {
		requests[index] = copySpeechRequest(request)
	}
	return requests
}

func copySpeechRequest(request speech.Request) speech.Request {
	request.Segment.SectionPath = append([]string(nil), request.Segment.SectionPath...)
	request.Segment.SourceBlocks = append([]uint64(nil), request.Segment.SourceBlocks...)
	return request
}

type FakeTranslator struct {
	DelayByID     map[string]time.Duration
	Delay         time.Duration
	Fail          map[string]error
	TranslateText func(segment.Segment, string) string
	Remote        bool
	BackendID     string
	LanguagePairs map[string]bool

	mu           sync.Mutex
	Calls        int
	Requests     []translate.Request
	CompletedIDs []string
}

func (f *FakeTranslator) ID() string {
	if f.BackendID != "" {
		return f.BackendID
	}
	return "fake-translation"
}

func (f *FakeTranslator) Capabilities(context.Context) (translate.Capabilities, error) {
	return translate.Capabilities{Name: f.ID(), Local: !f.Remote, Remote: f.Remote, LanguagePairs: f.LanguagePairs, SupportsBatch: true}, nil
}

func (f *FakeTranslator) Translate(ctx context.Context, request translate.Request) ([]translate.Result, error) {
	f.mu.Lock()
	f.Calls++
	f.Requests = append(f.Requests, copyTranslationRequest(request))
	f.mu.Unlock()
	results := make([]translate.Result, 0, len(request.Segments))
	for _, item := range request.Segments {
		delay := f.DelayByID[item.ID]
		if delay <= 0 {
			delay = f.Delay
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		if err := f.Fail[item.ID]; err != nil {
			return nil, err
		}
		text := item.SourceText
		if f.TranslateText != nil {
			text = f.TranslateText(item, request.TargetLanguage)
		}
		f.mu.Lock()
		f.CompletedIDs = append(f.CompletedIDs, item.ID)
		f.mu.Unlock()
		results = append(results, translate.Result{SegmentID: item.ID, Text: text})
	}
	return results, nil
}

func (f *FakeTranslator) Close() error { return nil }

func (f *FakeTranslator) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Calls
}

func (f *FakeTranslator) RequestSnapshot() []translate.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := make([]translate.Request, len(f.Requests))
	for index, request := range f.Requests {
		requests[index] = copyTranslationRequest(request)
	}
	return requests
}

func (f *FakeTranslator) CompletionSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.CompletedIDs...)
}

func copyTranslationRequest(request translate.Request) translate.Request {
	request.Segments = append([]segment.Segment(nil), request.Segments...)
	for index := range request.Segments {
		request.Segments[index].SectionPath = append([]string(nil), request.Segments[index].SectionPath...)
		request.Segments[index].SourceBlocks = append([]uint64(nil), request.Segments[index].SourceBlocks...)
	}
	if request.Glossary != nil {
		glossary := make(map[string]string, len(request.Glossary))
		for source, translated := range request.Glossary {
			glossary[source] = translated
		}
		request.Glossary = glossary
	}
	return request
}

var _ speech.Synthesizer = (*FakeSynthesizer)(nil)
var _ translate.Translator = (*FakeTranslator)(nil)
