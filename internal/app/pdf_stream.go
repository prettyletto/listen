package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/pipeline"
	"github.com/prettyletto/listen/internal/playerui"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/session"
	"github.com/prettyletto/listen/internal/source"
	pdfsource "github.com/prettyletto/listen/internal/source/pdf"
	textsource "github.com/prettyletto/listen/internal/source/text"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
	"github.com/prettyletto/listen/internal/ui"
)

func runScannedPDFPlayback(
	ctx context.Context,
	stdin io.Reader,
	stderr io.Writer,
	deps runtimeDeps,
	options listenOptions,
	input source.Input,
	cacheRoot string,
	manifests *cache.FileStore,
	structureKey string,
	reusable bool,
	book *pdfsource.PageSource,
	terminal ui.Output,
	progressStatus *func(string),
	progressActive *bool,
) error {
	defer book.Close()
	if err := terminal.Info("Using local OCR. Playback will start as pages are recognized."); err != nil {
		return err
	}
	audioCache, err := speech.NewCache(cacheRoot)
	if err != nil {
		return apperr.New("storage_unavailable", "Could not prepare local audio storage.", "Check that your cache directory is writable.", err)
	}
	synthesizer := &deferredSpeechSynthesizer{
		start:   deps.startSynth,
		options: kokoro.Options{CacheRoot: cacheRoot, Voice: options.voice},
	}
	defer synthesizer.Close()

	title := docTitle(book.Metadata().Title, input.Name)
	placeholder := segment.Segment{ID: "streaming-pdf", Seq: 0, MajorUnitID: book.DocumentID(), SectionPath: []string{title}}
	plan := segment.Plan{DocumentID: book.DocumentID(), Segments: []segment.Segment{placeholder}}
	chapters := []playerui.Chapter{{ID: book.DocumentID(), Title: "Reading order", StartIndex: 0, StartSeq: 0}}
	start := playerui.StartPoint{Label: "Start from the beginning", StartIndex: 0, StartSeq: 0}
	startupBuffer := deps.startupBuffer
	if startupBuffer > 3*time.Second {
		startupBuffer = 3 * time.Second
	}
	streamOptions := pipeline.StreamOptions{
		Workers: 1, QueueCapacity: 2, StartupBuffer: startupBuffer,
		Voice: options.voice, Speed: options.speed,
	}

	var pageWarnings []string
	var nextSequence uint64
	var completeDocument *domain.Document
	var persistenceErr error
	liveProducer := func(produceCtx context.Context, emit func(segment.Segment) error, sourceBoundary func() error, reportStatus func(string)) error {
		if progressStatus != nil {
			*progressStatus = reportStatus
		}
		defer func() {
			if progressStatus != nil {
				*progressStatus = nil
			}
		}()
		err := book.StreamPages(produceCtx, func(page pdfsource.PageResult) error {
			if page.Warning != "" {
				pageWarnings = append(pageWarnings, page.Warning)
				if reportStatus != nil {
					reportStatus("Warning: " + page.Warning)
				}
			}
			pagePlan, err := segment.PlanDocument(&page.Document, segment.Options{})
			if err != nil {
				return err
			}
			pageFirstSequence := nextSequence
			for _, item := range pagePlan.Segments {
				item.Seq = nextSequence
				nextSequence++
				if err := emit(item); err != nil {
					return err
				}
			}
			if nextSequence > pageFirstSequence {
				if err := sourceBoundary(); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if nextSequence == 0 {
			return textsource.ErrEmptySource
		}
		if progressActive != nil {
			*progressActive = false
		}
		if progressStatus != nil {
			*progressStatus = nil
		}
		completeDocument, _ = book.CompleteDocument()
		if completeDocument == nil {
			return errors.New("PDF page stream completed without a document")
		}
		if reusable {
			if err := saveCachedDocument(produceCtx, manifests, structureKey, completeDocument); err != nil {
				persistenceErr = apperr.New("storage_unavailable", "Could not save the PDF reading structure.", "Check that your local cache directory is writable.", err)
			}
		}
		if _, err := planCachedSegments(produceCtx, completeDocument, input.Path, manifests); err != nil {
			if persistenceErr == nil {
				persistenceErr = apperr.New("segment_failed", "Could not save the recognized PDF segment plan for reuse.", "Check the source file and local cache, then run listen again.", err)
			}
		}
		return nil
	}

	_, stopped, err := runPlayback(ctx, stdin, stderr, deps, options, title, plan, start, chapters,
		synthesizer, audioCache, streamOptions, nil, session.State{
			DocumentID: book.DocumentID(), PlaybackSpeed: options.playbackSpeed, Voice: options.voice,
		}, nil, liveProducer)
	if progressActive != nil {
		*progressActive = false
	}
	if progressStatus != nil {
		*progressStatus = nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return interrupted(ctx.Err())
		}
		var appError *apperr.Error
		if errors.As(err, &appError) {
			return appError
		}
		if isPDFReadError(err) {
			return pdfSourceError(ctx, err)
		}
		return playerError(err)
	}
	if stopped {
		_ = terminal.Info("Playback stopped. Recognized pages and completed audio are cached; run listen again to reuse them.")
		if persistenceErr != nil {
			return persistenceErr
		}
		return nil
	}
	if persistenceErr != nil {
		return persistenceErr
	}

	if completeDocument == nil {
		return pdfSourceError(ctx, textsource.ErrEmptySource)
	}
	for _, warning := range completeDocument.Warnings {
		if containsString(pageWarnings, warning) {
			continue
		}
		if err := terminal.Warning(warning); err != nil {
			return err
		}
	}
	return nil
}

type deferredSpeechSynthesizer struct {
	start   func(context.Context, kokoro.Options) (speech.Synthesizer, error)
	options kokoro.Options
	once    sync.Once
	worker  speech.Synthesizer
	err     error
}

func (s *deferredSpeechSynthesizer) ID() string { return kokoro.EngineID }

func (s *deferredSpeechSynthesizer) Synthesize(ctx context.Context, request speech.Request) (speech.Result, error) {
	s.once.Do(func() {
		s.worker, s.err = s.start(ctx, s.options)
	})
	if s.err != nil {
		if ctx.Err() != nil {
			return speech.Result{}, ctx.Err()
		}
		return speech.Result{}, apperr.New("speech_setup_required", "Local speech is not ready.", "Run listen setup to install the speech engine and voice.", s.err)
	}
	if s.worker == nil {
		return speech.Result{}, apperr.New("speech_setup_required", "Local speech is not ready.", "Run listen setup to install the speech engine and voice.", errors.New("speech runtime did not start"))
	}
	return s.worker.Synthesize(ctx, request)
}

func (s *deferredSpeechSynthesizer) Close() error {
	if s.worker == nil {
		return nil
	}
	return s.worker.Close()
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isPDFReadError(err error) bool {
	return errors.Is(err, pdfsource.ErrReaderUnavailable) || errors.Is(err, pdfsource.ErrOCRUnavailable) ||
		errors.Is(err, pdfsource.ErrOCRLanguageUnavailable) || errors.Is(err, pdfsource.ErrOCRFailed) ||
		errors.Is(err, pdfsource.ErrResourceLimit) || errors.Is(err, textsource.ErrEmptySource)
}

func pdfSourceError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return interrupted(ctx.Err())
	}
	switch {
	case errors.Is(err, pdfsource.ErrReaderUnavailable):
		return apperr.New("pdf_reader_unavailable", "PDF reading tools are unavailable on this computer.", "Install Poppler utilities, including pdftoppm, then run listen again.", err)
	case errors.Is(err, pdfsource.ErrOCRLanguageUnavailable):
		return apperr.New("pdf_ocr_language_unavailable", "The configured PDF OCR language data is not installed.", "Install the matching Tesseract language data or run listen config set ocr-languages eng.", err)
	case errors.Is(err, pdfsource.ErrOCRUnavailable):
		return apperr.New("pdf_ocr_unavailable", "Local OCR is unavailable on this computer.", "Install Tesseract OCR with trained data for the configured language, then run listen again.", err)
	case errors.Is(err, pdfsource.ErrOCRFailed):
		return apperr.New("pdf_ocr_failed", "A scanned PDF page could not be recognized.", "Check scan quality and page rotation, then try a clearer copy of the PDF.", err)
	case errors.Is(err, pdfsource.ErrResourceLimit):
		return apperr.New("pdf_resource_limit", "This PDF exceeds the reader's processing limits.", "Split the PDF into smaller files and try again.", err)
	case errors.Is(err, textsource.ErrEmptySource):
		return apperr.New("pdf_no_readable_text", "No readable text was found in this PDF.", "Choose a PDF with readable text or improve the scan quality and try again.", err)
	default:
		return apperr.New("source_read_failed", "Could not read this PDF document.", "Check that the file is a valid, readable PDF and try again.", err)
	}
}
