package app

import (
	"context"
	"errors"
	"fmt"
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
	if !deps.interactivePlayback(stdin, stderr) {
		if err := terminal.Info("Using local OCR. Playback will start as pages are recognized."); err != nil {
			return err
		}
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
	chapters := []playerui.Chapter{{ID: book.DocumentID(), Title: "Reading order", StartIndex: 0, StartSeq: 0}}
	var sessionStore *session.Store
	baseState := session.State{DocumentID: book.DocumentID(), PlaybackSpeed: options.playbackSpeed, Voice: options.voice}
	var savedState *session.State
	if !options.noPlay && !options.exportAudio && options.source != "-" {
		store, storeErr := session.NewStore(cacheRoot, input.Path)
		if storeErr != nil {
			_ = terminal.Warning("Playback position storage is unavailable; listening will continue without resume.")
		} else {
			sessionStore = store
			state, found, loadErr := store.Load(ctx)
			if loadErr != nil {
				_ = terminal.Warning("The saved PDF position could not be read; starting at the beginning.")
			} else if found && !state.Complete && state.TargetLanguage == "" && state.TranslationBackend == "" {
				savedState = &state
			}
		}
	}
	var playbackUI playerScreen
	uiOwned := false
	if savedState != nil && deps.interactivePlayback(stdin, stderr) {
		model := playerui.NewPlayer(title, chapters, 0, 0)
		model.SetStatus("Restoring your place · checking saved pages · Esc to cancel")
		display, screenErr := deps.startPlaybackScreen(stdin, stderr, model)
		if screenErr != nil {
			return apperr.New("player_unavailable", "Could not open the playback controls.", "Run listen from a Linux terminal, or use --no-play to prepare audio without playback.", screenErr)
		}
		playbackUI = display
		uiOwned = true
		defer func() {
			if uiOwned && playbackUI != nil {
				playbackUI.Close()
			}
		}()
	}
	startSeq := uint64(0)
	startPage := 0
	var resumeOffset time.Duration
	restoreMessage := ""
	if savedState != nil {
		location, end, cancelled, resolveErr := restorePDFPosition(ctx, book, *savedState, playbackUI, terminal)
		if cancelled {
			if deps.playbackEnd != nil {
				*deps.playbackEnd = end
			}
			return nil
		}
		if resolveErr != nil {
			if ctx.Err() != nil {
				return interrupted(ctx.Err())
			}
			return pdfSourceError(ctx, resolveErr)
		}
		if location.Found {
			startSeq = location.Segment.Seq
			startPage = location.Segment.Source.Page
			resumeOffset = location.Offset
			if !location.ChangedContent {
				restoreMessage = fmt.Sprintf("Resuming at PDF page %d", location.Segment.Source.Page)
			} else {
				restoreMessage = fmt.Sprintf("Your saved passage changed; continuing at PDF page %d from its beginning", location.Segment.Source.Page)
			}
		} else {
			restoreMessage = "The saved passage is unavailable; starting from the beginning"
		}
		baseState.DocumentID = book.DocumentID()
	}
	placeholder := segment.Segment{ID: "streaming-pdf", Seq: startSeq, MajorUnitID: book.DocumentID(), SectionPath: []string{title}, Source: domain.SourceRef{Page: startPage}}
	plan := segment.Plan{DocumentID: book.DocumentID(), Segments: []segment.Segment{placeholder}}
	chapters[0].StartSeq = startSeq
	chapters[0].Page = startPage
	start := playerui.StartPoint{Label: "Start from the beginning", StartIndex: 0, StartSeq: startSeq, Offset: resumeOffset, Resume: resumeOffset > 0 || savedState != nil, Page: startPage}
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
	var allSegments []segment.Segment
	resolver := session.NewPositionResolver()
	var completeDocument *domain.Document
	var persistenceErr error
	var targetFound = startSeq == 0
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
			pageHadEmission := false
			for pageSegment, item := range pagePlan.Segments {
				item.Seq = nextSequence
				if item.Source.Page == 0 {
					item.Source.Page = page.Page
				}
				nextSequence++
				allSegments = append(allSegments, item)
				if item.Seq < startSeq {
					if reportStatus != nil && savedState != nil {
						reportStatus(fmt.Sprintf("Restoring your place · checking PDF page %d/%d", page.Page, book.PageCount()))
					}
					continue
				}
				resolver.Add(item, pageSegment)
				if item.Seq == startSeq {
					targetFound = true
					if reportStatus != nil && restoreMessage != "" {
						reportStatus(restoreMessage)
					}
				}
				if err := emit(item); err != nil {
					return err
				}
				pageHadEmission = true
			}
			if nextSequence > pageFirstSequence && pageHadEmission {
				if err := sourceBoundary(); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if nextSequence == 0 || !targetFound {
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

	fullReport, end, err := runPlayback(ctx, stdin, stderr, deps, options, title, plan, start, chapters,
		synthesizer, audioCache, streamOptions, sessionStore, baseState, &playbackUI, liveProducer, resolver.Record)
	uiOwned = false
	if deps.playbackEnd != nil {
		*deps.playbackEnd = end
	}
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
	if end == PlaybackStopped || end == PlaybackReturnedHome {
		if !deps.interactivePlayback(stdin, stderr) {
			_ = terminal.Info("Playback stopped. Your place, recognized pages, and completed audio are saved for next time.")
		}
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
	if sessionStore != nil && len(allSegments) > 0 {
		last := allSegments[len(allSegments)-1]
		finalPlan := segment.Plan{DocumentID: book.DocumentID(), Segments: allSegments}
		state, ok := session.RecordPosition(baseState, finalPlan, last.Seq, lastResultDuration(fullReport, last.Seq))
		if ok {
			state.Complete = true
			if saveErr := sessionStore.Save(context.WithoutCancel(ctx), state); saveErr != nil {
				return apperr.New("session_save_failed", "Playback finished, but its completion state could not be saved.", "Completed audio remains in the local cache.", saveErr)
			}
		}
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

var errPDFRestoreScanComplete = errors.New("PDF resume location resolved")

type pdfResumeLocation struct {
	Segment        segment.Segment
	Offset         time.Duration
	Found          bool
	Exact          bool
	ChangedContent bool
}

type pdfRestoreResult struct {
	location pdfResumeLocation
	err      error
}

type pdfPageSegments struct {
	Page     int
	Segments []segment.Segment
}

// restorePDFPosition runs the cached-page mapping pass while the player screen
// remains responsive. Escape cancels OCR and returns home; q cancels and exits.
func restorePDFPosition(ctx context.Context, book *pdfsource.PageSource, saved session.State, display playerScreen, terminal ui.Output) (pdfResumeLocation, PlaybackEnd, bool, error) {
	return awaitPDFRestore(ctx, display, func(scanCtx context.Context, update func(string)) (pdfResumeLocation, error) {
		return resolvePDFResume(scanCtx, book, saved, book.DocumentID(), func(page, total int) {
			status := fmt.Sprintf("Restoring your place · checking PDF page %d/%d", page, total)
			if display != nil {
				update(status)
			} else if page == 1 || page%10 == 0 || page == total {
				_ = terminal.Info(status)
			}
		})
	})
}

func awaitPDFRestore(ctx context.Context, display playerScreen, scan func(context.Context, func(string)) (pdfResumeLocation, error)) (pdfResumeLocation, PlaybackEnd, bool, error) {
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := make(chan string, 1)
	result := make(chan pdfRestoreResult, 1)
	go func() {
		location, err := scan(scanCtx, func(status string) {
			if display != nil {
				select {
				case updates <- status:
				default:
					select {
					case <-updates:
					default:
					}
					select {
					case updates <- status:
					default:
					}
				}
			}
		})
		result <- pdfRestoreResult{location: location, err: err}
	}()
	var actions <-chan playerui.Action
	if display != nil {
		actions = display.Actions()
	}
	for {
		select {
		case status := <-updates:
			if display != nil {
				display.SetStatus(status)
			}
		case action := <-actions:
			switch action.Kind {
			case playerui.ActionReturnHome:
				if display != nil {
					display.SetStatus("Returning to your library")
				}
				cancel()
				<-result
				return pdfResumeLocation{}, PlaybackReturnedHome, true, nil
			case playerui.ActionStop:
				if display != nil {
					display.SetFinished("Playback stopped")
				}
				cancel()
				<-result
				return pdfResumeLocation{}, PlaybackStopped, true, nil
			}
		case completed := <-result:
			if completed.err != nil && ctx.Err() != nil {
				return pdfResumeLocation{}, PlaybackCompleted, false, ctx.Err()
			}
			return completed.location, PlaybackCompleted, false, completed.err
		case <-ctx.Done():
			cancel()
			<-result
			return pdfResumeLocation{}, PlaybackCompleted, false, ctx.Err()
		}
	}
}

func resolvePDFResume(ctx context.Context, book *pdfsource.PageSource, saved session.State, documentID string, progress func(page, total int)) (pdfResumeLocation, error) {
	sameDocument := saved.DocumentID != "" && saved.DocumentID == documentID
	var nextSequence uint64
	var pages []pdfPageSegments
	var resolved pdfResumeLocation
	err := book.StreamPages(ctx, func(page pdfsource.PageResult) error {
		if progress != nil {
			progress(page.Page, book.PageCount())
		}
		pagePlan, err := segment.PlanDocument(&page.Document, segment.Options{})
		if err != nil {
			return err
		}
		pageSegments := make([]segment.Segment, len(pagePlan.Segments))
		for index, item := range pagePlan.Segments {
			item.Seq = nextSequence
			if item.Source.Page == 0 {
				item.Source.Page = page.Page
			}
			nextSequence++
			pageSegments[index] = item
			if sameDocument && saved.SegmentID != "" && item.ID == saved.SegmentID {
				resolved = pdfResumeLocation{Segment: item, Offset: saved.Offset, Found: true, Exact: true}
				return errPDFRestoreScanComplete
			}
		}
		if len(pageSegments) > 0 {
			pages = append(pages, pdfPageSegments{Page: page.Page, Segments: pageSegments})
		}
		if saved.Page > 0 {
			if page.Page == saved.Page && len(pageSegments) > 0 {
				resolved = fallbackPDFResume(nearestPDFPagePassage(pages, saved.Page, saved.PageSegment), saved, sameDocument)
				return errPDFRestoreScanComplete
			}
			if page.Page > saved.Page && len(pageSegments) > 0 {
				resolved = fallbackPDFResume(nearestPDFPagePassage(pages, saved.Page, saved.PageSegment), saved, sameDocument)
				return errPDFRestoreScanComplete
			}
			return nil
		}
		if nextSequence > saved.SegmentSeq {
			resolved = fallbackPDFResume(nearestPDFSequencePassage(pages, saved.SegmentSeq), saved, sameDocument)
			return errPDFRestoreScanComplete
		}
		return nil
	})
	if err != nil && !errors.Is(err, errPDFRestoreScanComplete) {
		return pdfResumeLocation{}, err
	}
	if resolved.Found {
		return resolved, nil
	}
	if len(pages) > 0 {
		var fallback pdfResumeLocation
		if saved.Page > 0 {
			fallback = nearestPDFPagePassage(pages, saved.Page, saved.PageSegment)
		} else {
			fallback = nearestPDFSequencePassage(pages, saved.SegmentSeq)
		}
		return fallbackPDFResume(fallback, saved, sameDocument), nil
	}
	return pdfResumeLocation{}, nil
}

func fallbackPDFResume(location pdfResumeLocation, saved session.State, sameDocument bool) pdfResumeLocation {
	location.Exact = false
	location.Offset = 0
	location.ChangedContent = !sameDocument
	if sameDocument && location.Found {
		location.Offset = saved.Offset
	}
	return location
}

func nearestPDFPagePassage(pages []pdfPageSegments, page, pageSegment int) pdfResumeLocation {
	bestDistance := int(^uint(0) >> 1)
	var chosen segment.Segment
	found := false
	for _, candidatePage := range pages {
		if len(candidatePage.Segments) == 0 {
			continue
		}
		distance := candidatePage.Page - page
		if distance < 0 {
			distance = -distance
		}
		if distance > bestDistance || (distance == bestDistance && candidatePage.Page > chosen.Source.Page) {
			continue
		}
		index := pageSegment
		if index < 0 {
			index = 0
		}
		if index >= len(candidatePage.Segments) {
			index = len(candidatePage.Segments) - 1
		}
		chosen = candidatePage.Segments[index]
		bestDistance, found = distance, true
	}
	return pdfResumeLocation{Segment: chosen, Found: found}
}

func nearestPDFSequencePassage(pages []pdfPageSegments, sequence uint64) pdfResumeLocation {
	var chosen segment.Segment
	var bestDistance uint64 = ^uint64(0)
	found := false
	for _, page := range pages {
		for _, item := range page.Segments {
			distance := item.Seq
			if distance >= sequence {
				distance -= sequence
			} else {
				distance = sequence - distance
			}
			if distance < bestDistance || (distance == bestDistance && item.Seq < chosen.Seq) {
				chosen, bestDistance, found = item, distance, true
			}
		}
	}
	return pdfResumeLocation{Segment: chosen, Found: found}
}

func lastResultDuration(report pipeline.StreamReport, sequence uint64) time.Duration {
	for index := len(report.Results) - 1; index >= 0; index-- {
		if report.Results[index].Seq == sequence {
			return report.Results[index].Duration
		}
	}
	return 0
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
