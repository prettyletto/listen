package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/prettyletto/listen/internal/speech"
)

type synthesizerStarter func(context.Context) (speech.Synthesizer, error)

// restartableSynthesizer replaces a worker after a canceled inference request.
// The Kokoro protocol cannot cancel one request independently of its process,
// so a chapter change may stop the current worker while the next chapter still
// needs speech generation.
type restartableSynthesizer struct {
	mu      sync.Mutex
	ctx     context.Context
	start   synthesizerStarter
	current speech.Synthesizer
	id      string
	closed  bool
}

func newRestartableSynthesizer(ctx context.Context, initial speech.Synthesizer, start synthesizerStarter) *restartableSynthesizer {
	if ctx == nil {
		ctx = context.Background()
	}
	id := ""
	if initial != nil {
		id = initial.ID()
	}
	return &restartableSynthesizer{ctx: ctx, start: start, current: initial, id: id}
}

func (s *restartableSynthesizer) ID() string { return s.id }

func (s *restartableSynthesizer) Synthesize(ctx context.Context, request speech.Request) (speech.Result, error) {
	if ctx == nil {
		return speech.Result{}, errors.New("synthesis requires a context")
	}
	if err := ctx.Err(); err != nil {
		return speech.Result{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return speech.Result{}, errors.New("speech synthesizer is closed")
	}
	if err := ctx.Err(); err != nil {
		return speech.Result{}, err
	}
	if err := s.ctx.Err(); err != nil {
		return speech.Result{}, err
	}
	if s.current == nil {
		if s.start == nil {
			return speech.Result{}, errors.New("speech worker cannot restart after an interrupted request")
		}
		worker, err := s.start(s.ctx)
		if err != nil {
			return speech.Result{}, fmt.Errorf("restart local speech worker: %w", err)
		}
		if worker == nil {
			return speech.Result{}, errors.New("restart local speech worker returned no worker")
		}
		if worker.ID() != s.id {
			_ = worker.Close()
			return speech.Result{}, errors.New("restart local speech worker returned a different engine")
		}
		s.current = worker
	}

	result, err := s.current.Synthesize(ctx, request)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		worker := s.current
		s.current = nil
		if closeErr := worker.Close(); closeErr != nil {
			return result, errors.Join(err, fmt.Errorf("close interrupted speech worker: %w", closeErr))
		}
	}
	return result, err
}

func (s *restartableSynthesizer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.current == nil {
		return nil
	}
	return s.current.Close()
}
