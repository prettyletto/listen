// Package pdf converts born-digital PDF text and page layout into the canonical
// document IR. It uses Poppler for positioned text extraction and treats the
// inferred reading order as best effort.
package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
	textsource "github.com/prettyletto/listen/internal/source/text"
)

const parserVersion = "pdf-layout-v1"

var (
	ErrReaderUnavailable = errors.New("PDF text extraction tools are unavailable")
	ErrNeedsOCR          = errors.New("PDF appears to contain scanned pages without readable text")
	ErrResourceLimit     = errors.New("PDF exceeds a processing resource limit")
)

const (
	maxPDFBytes         = int64(256 << 20)
	maxPDFPages         = 5000
	maxPDFTextFragments = 500_000
	maxPDFFontSpecs     = 100_000
	maxPDFImages        = 100_000
	maxPDFBookmarks     = 50_000
	maxPDFBookmarkDepth = 64
	maxXMLBytes         = 64 << 20
	maxJSONBytes        = 16 << 20
	maxImageListBytes   = 16 << 20
	maxStderrBytes      = 32 << 10
	maxToolRuntime      = 2 * time.Minute
)

type commandRunner func(context.Context, string, ...string) ([]byte, error)
type commandRunnerInDir func(context.Context, string, string, ...string) ([]byte, error)

// Parser converts born-digital PDF content into the shared Document IR.
type Parser struct {
	run    commandRunner
	runDir commandRunnerInDir
}

func NewParser() Parser { return Parser{run: runCommand, runDir: runCommandInDir} }

func (Parser) Name() string { return "PDF" }

func (Parser) Probe(ctx context.Context, input source.Input) (int, error) {
	if ctx == nil {
		return 0, errors.New("PDF parser requires a context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	name := input.Name
	if name == "" {
		name = input.Path
	}
	if strings.EqualFold(filepath.Ext(name), ".pdf") {
		return 100, nil
	}
	if hasPDFSignature(input) {
		return 80, nil
	}
	return 0, nil
}

func (p Parser) Parse(ctx context.Context, input source.Input) (*domain.Document, error) {
	if ctx == nil {
		return nil, errors.New("PDF parser requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path, cleanup, err := materializeInput(ctx, input)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve PDF source path: %w", err)
	}

	if _, err := exec.LookPath("pdftohtml"); err != nil {
		return nil, fmt.Errorf("%w: install Poppler utilities and try again", ErrReaderUnavailable)
	}
	if _, err := exec.LookPath("pdfimages"); err != nil {
		return nil, fmt.Errorf("%w: install Poppler utilities and try again", ErrReaderUnavailable)
	}
	run := p.run
	if run == nil {
		run = runCommand
	}
	outputDir, err := os.MkdirTemp("", "listen-pdf-output-*")
	if err != nil {
		return nil, fmt.Errorf("prepare temporary PDF output directory: %w", err)
	}
	defer os.RemoveAll(outputDir)
	outputPrefix := filepath.Join(outputDir, "page")
	args := []string{"-q", "-xml", "-noroundcoord", "-zoom", "1", "-hidden", "-nomerge", "-i", "-stdout", path, outputPrefix}
	var xmlData []byte
	if p.runDir != nil {
		xmlData, err = p.runDir(ctx, outputDir, "pdftohtml", args...)
	} else if p.run != nil {
		xmlData, err = run(ctx, "pdftohtml", args...)
	} else {
		xmlData, err = runCommandInDir(ctx, outputDir, "pdftohtml", args...)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("extract positioned PDF text: %w", err)
	}
	if len(xmlData) == 0 {
		return nil, textsource.ErrEmptySource
	}
	pages, err := parseLayoutXML(ctx, xmlData)
	if err != nil {
		return nil, fmt.Errorf("read PDF text layout: %w", err)
	}
	if len(pages) == 0 {
		return nil, textsource.ErrEmptySource
	}
	if len(pages) > maxPDFPages {
		return nil, fmt.Errorf("%w: PDF has more than %d pages", ErrResourceLimit, maxPDFPages)
	}
	imageList, err := run(ctx, "pdfimages", "-list", path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("inspect PDF page images: %w", err)
	}
	imagesByPage, err := parseImageList(imageList)
	if err != nil {
		return nil, fmt.Errorf("read PDF image listing: %w", err)
	}

	pageHasText := 0
	scannedPages := 0
	for i := range pages {
		pages[i].images = imagesByPage[pages[i].number]
		pages[i].lines = groupTextLines(pages[i])
		if len(pages[i].lines) > 0 {
			pageHasText++
		}
		if looksScanned(pages[i]) {
			scannedPages++
		}
	}
	if scannedPages > 0 && (pageHasText == 0 || scannedPages*2 >= len(pages)) {
		return nil, ErrNeedsOCR
	}

	pathForSource := input.Path
	if pathForSource == "" {
		pathForSource = input.Name
	}
	if pathForSource != "" {
		if absolute, absErr := filepath.Abs(pathForSource); absErr == nil {
			pathForSource = absolute
		}
	}
	contentHash, err := hashFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("identify PDF source: %w", err)
	}
	docID := stableID(parserVersion, contentHash)
	title := strings.TrimSpace(strings.TrimSuffix(filepath.Base(pathForSource), filepath.Ext(pathForSource)))
	if title == "" || title == "." {
		title = "Untitled PDF"
	}
	info := readPDFInfo(ctx, run, path)
	if info.metadata.Title != "" {
		title = info.metadata.Title
	}

	blocks := makeBlocks(ctx, pages, pathForSource)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, textsource.ErrEmptySource
	}

	doc := &domain.Document{
		ID:       docID,
		Format:   domain.FormatPDF,
		Metadata: info.metadata,
		Blocks:   blocks,
	}
	if doc.Metadata.Title == "" {
		doc.Metadata.Title = title
	}
	addWarning(&doc.Warnings, "PDF text and reading order were reconstructed from page geometry; complex layouts may differ from the source.")
	if info.tagged {
		addWarning(&doc.Warnings, "This PDF contains tagged structure that was not used; bookmarks and page layout determined its reading order.")
	}
	if scannedPages > 0 {
		addWarning(&doc.Warnings, "Some PDF pages appear to be scanned and were omitted because OCR is not available.")
	}

	bookmarks, bookmarkWarning, err := readBookmarks(ctx, run, path)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if len(bookmarks) > 0 {
		doc.Outline = outlineFromBookmarks(bookmarks, doc.Blocks, pathForSource, docID)
		if len(doc.Outline) == 0 {
			addWarning(&doc.Warnings, "PDF bookmarks could not be matched to readable pages; visual layout was used instead.")
		}
	}
	if len(doc.Outline) == 0 {
		doc.Outline = outlineFromHeadings(doc.Blocks, pathForSource, docID)
		if bookmarkWarning != "" {
			addWarning(&doc.Warnings, bookmarkWarning)
		}
		if len(doc.Outline) == 0 {
			addWarning(&doc.Warnings, "No reliable section headings were found in this PDF; reading order follows page layout.")
		}
	}
	return doc, nil
}

func materializeInput(ctx context.Context, input source.Input) (string, func(), error) {
	if input.Path != "" {
		info, err := os.Stat(input.Path)
		if err != nil {
			return "", func() {}, err
		}
		if !info.Mode().IsRegular() {
			return "", func() {}, fmt.Errorf("PDF source %q is not a regular file", input.Path)
		}
		if err := checkPDFSize(info.Size()); err != nil {
			return "", func() {}, err
		}
		return input.Path, func() {}, nil
	}
	if input.R == nil {
		return "", func() {}, errors.New("PDF source has no reader or path")
	}
	file, err := os.CreateTemp("", "listen-pdf-*.pdf")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary PDF: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	copied, copyErr := copyAtMost(ctx, file, input.R, maxPDFBytes)
	closeErr := file.Close()
	if copyErr != nil {
		cleanup()
		if ctx.Err() != nil {
			return "", func() {}, ctx.Err()
		}
		return "", func() {}, fmt.Errorf("read PDF source: %w", copyErr)
	}
	if err := checkPDFSize(copied); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if closeErr != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close temporary PDF: %w", closeErr)
	}
	return path, cleanup, nil
}

func copyAtMost(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: src}, N: limit + 1}
	copied, err := io.Copy(dst, limited)
	if err != nil {
		return copied, err
	}
	if copied > limit {
		return copied, fmt.Errorf("%w: input is larger than %d bytes", ErrResourceLimit, limit)
	}
	return copied, nil
}

func checkPDFSize(size int64) error {
	if size > maxPDFBytes {
		return fmt.Errorf("%w: PDF is larger than %d MiB", ErrResourceLimit, maxPDFBytes>>20)
	}
	return nil
}

func hasPDFSignature(input source.Input) bool {
	if input.Path != "" {
		file, err := os.Open(input.Path)
		if err != nil {
			return false
		}
		defer file.Close()
		var signature [5]byte
		count, err := file.Read(signature[:])
		return err == nil && count == len(signature) && bytes.Equal(signature[:], []byte("%PDF-"))
	}
	if readerAt, ok := input.R.(io.ReaderAt); ok {
		var signature [5]byte
		count, err := readerAt.ReadAt(signature[:], 0)
		return err == nil && count == len(signature) && bytes.Equal(signature[:], []byte("%PDF-"))
	}
	if seeker, ok := input.R.(io.ReadSeeker); ok {
		position, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return false
		}
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return false
		}
		var signature [5]byte
		count, readErr := seeker.Read(signature[:])
		_, restoreErr := seeker.Seek(position, io.SeekStart)
		return readErr == nil && restoreErr == nil && count == len(signature) && bytes.Equal(signature[:], []byte("%PDF-"))
	}
	return false
}

func hashFile(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, reader: file}); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buffer.Write(p[:remaining])
			w.exceeded = true
			return remaining, errOutputLimitReached
		} else {
			_, _ = w.buffer.Write(p)
		}
	} else if len(p) > 0 {
		w.exceeded = true
		return 0, errOutputLimitReached
	}
	return len(p), nil
}

var errOutputLimitReached = errors.New("subprocess output limit reached")

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runCommandInDir(ctx, "", name, args...)
}

func runCommandInDir(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	limit := maxXMLBytes
	if name == "qpdf" {
		limit = maxJSONBytes
	} else if name == "pdfimages" {
		limit = maxImageListBytes
	} else if name == "pdfinfo" {
		limit = 1 << 20
	}
	stdout := &cappedBuffer{limit: limit}
	stderr := &cappedBuffer{limit: maxStderrBytes}
	commandCtx, cancel := context.WithTimeout(ctx, maxToolRuntime)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, fmt.Errorf("%w: %s output exceeds the supported size limit", ErrResourceLimit, filepath.Base(name))
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s exceeded the time limit", ErrResourceLimit, filepath.Base(name))
		}
		return nil, fmt.Errorf("%s exited unsuccessfully: %w: %s", filepath.Base(name), err, strings.TrimSpace(stderr.buffer.String()))
	}
	return stdout.buffer.Bytes(), nil
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func addWarning(warnings *[]string, warning string) {
	for _, existing := range *warnings {
		if existing == warning {
			return
		}
	}
	*warnings = append(*warnings, warning)
}

var _ source.Parser = Parser{}
