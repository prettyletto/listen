package pdf

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
	textsource "github.com/prettyletto/listen/internal/source/text"
)

const (
	defaultOCRLanguages = "eng"
	defaultOCRDPI       = 240
	ocrCacheVersion     = "tesseract-tsv-v1"
	minOCRConfidence    = 55.0
	maxOCRTSVBytes      = 16 << 20
)

var (
	ErrOCRUnavailable         = errors.New("local PDF OCR tools are unavailable")
	ErrOCRLanguageUnavailable = errors.New("a requested local OCR language is unavailable")
	ErrOCRFailed              = errors.New("local PDF OCR failed")
)

// OCRConfig controls local OCR. Cache is optional for library callers; the CLI
// supplies its atomic cache so interrupted books reuse completed pages.
type OCRConfig struct {
	Cache      cache.Store
	Languages  string
	DPI        int
	OnProgress func(OCRProgress)
}

type OCRProgress struct {
	Page            int
	TotalPages      int
	ScannedComplete int
	ScannedTotal    int
	CacheHit        bool
}

// PageResult is one page converted into the shared document IR. Scanned pages
// retain their OCR confidence so callers can report uncertainty to the reader.
type PageResult struct {
	Document   domain.Document
	Page       int
	Scanned    bool
	Confidence float64
	Warning    string
}

type ocrArtifact struct {
	Version string
	TSV     string
}

// PageSource owns a prepared PDF and its temporary files. It performs the
// inexpensive Poppler layout preflight once, then lets callers consume pages
// in source order as OCR completes.
type PageSource struct {
	path          string
	sourcePath    string
	cleanupInput  func()
	outputDir     string
	run           commandRunner
	runDir        commandRunnerInDir
	config        OCRConfig
	pages         []pageLayout
	scanned       []bool
	scannedCount  int
	documentID    string
	metadata      domain.Metadata
	tagged        bool
	bookmarks     []qpdfOutline
	bookmarkNotes string
	engineVersion string
	pageProgress  int
	complete      *domain.Document
	closed        bool
}

func (p Parser) Prepare(ctx context.Context, input source.Input, config OCRConfig) (*PageSource, error) {
	if ctx == nil {
		return nil, errors.New("PDF parser requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if config.Languages == "" {
		config.Languages = defaultOCRLanguages
	}
	if config.DPI == 0 {
		config.DPI = defaultOCRDPI
	}
	if config.DPI < 120 || config.DPI > 400 {
		return nil, errors.New("PDF OCR resolution must be between 120 and 400 DPI")
	}
	if err := validateLanguageList(config.Languages); err != nil {
		return nil, err
	}
	path, cleanup, err := materializeInput(ctx, input)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*PageSource, error) {
		cleanup()
		return nil, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return fail(fmt.Errorf("resolve PDF source path: %w", err))
	}
	if _, err := exec.LookPath("pdftohtml"); err != nil {
		return fail(fmt.Errorf("%w: install Poppler utilities and try again", ErrReaderUnavailable))
	}
	if _, err := exec.LookPath("pdfimages"); err != nil {
		return fail(fmt.Errorf("%w: install Poppler utilities and try again", ErrReaderUnavailable))
	}
	run := p.run
	if run == nil {
		run = runCommand
	}
	runDir := p.runDir
	if runDir == nil {
		runDir = runCommandInDir
	}
	outputDir, err := os.MkdirTemp("", "listen-pdf-output-*")
	if err != nil {
		return fail(fmt.Errorf("prepare temporary PDF output directory: %w", err))
	}
	pages, images, err := preflightPDF(ctx, path, outputDir, run, runDir)
	if err != nil {
		_ = os.RemoveAll(outputDir)
		return fail(err)
	}
	if len(pages) == 0 {
		_ = os.RemoveAll(outputDir)
		return fail(textsource.ErrEmptySource)
	}
	if len(pages) > maxPDFPages {
		_ = os.RemoveAll(outputDir)
		return fail(fmt.Errorf("%w: PDF has more than %d pages", ErrResourceLimit, maxPDFPages))
	}
	scanned := make([]bool, len(pages))
	scannedCount := 0
	for i := range pages {
		pages[i].images = images[pages[i].number]
		pages[i].lines = groupTextLines(pages[i])
		scanned[i] = looksScanned(pages[i])
		if scanned[i] {
			scannedCount++
		}
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
		_ = os.RemoveAll(outputDir)
		return fail(fmt.Errorf("identify PDF source: %w", err))
	}
	info := readPDFInfo(ctx, run, path)
	title := strings.TrimSpace(strings.TrimSuffix(filepath.Base(pathForSource), filepath.Ext(pathForSource)))
	if title == "" || title == "." {
		title = "Untitled PDF"
	}
	if info.metadata.Title == "" {
		info.metadata.Title = title
	}
	bookmarks, bookmarkNotes, err := readBookmarks(ctx, run, path)
	if err != nil {
		_ = os.RemoveAll(outputDir)
		return fail(err)
	}
	book := &PageSource{
		path: path, sourcePath: pathForSource, cleanupInput: cleanup, outputDir: outputDir,
		run: run, runDir: runDir, config: config, pages: pages, scanned: scanned,
		scannedCount: scannedCount, documentID: stableID(parserVersion, contentHash),
		metadata: info.metadata, tagged: info.tagged, bookmarks: bookmarks,
		bookmarkNotes: bookmarkNotes,
	}
	if scannedCount > 0 {
		book.engineVersion, err = inspectOCRTools(ctx, run, config.Languages)
		if err != nil {
			book.Close()
			return nil, err
		}
	}
	return book, nil
}

func (p Parser) ParseWithOCR(ctx context.Context, input source.Input, config OCRConfig) (*domain.Document, error) {
	book, err := p.Prepare(ctx, input, config)
	if err != nil {
		return nil, err
	}
	defer book.Close()
	return book.Parse(ctx)
}

func preflightPDF(ctx context.Context, path, outputDir string, run commandRunner, runDir commandRunnerInDir) ([]pageLayout, map[int][]pdfImage, error) {
	outputPrefix := filepath.Join(outputDir, "page")
	args := []string{"-q", "-xml", "-noroundcoord", "-zoom", "1", "-hidden", "-nomerge", "-i", "-stdout", path, outputPrefix}
	var xmlData []byte
	var err error
	if runDir != nil {
		xmlData, err = runDir(ctx, outputDir, "pdftohtml", args...)
	} else {
		xmlData, err = run(ctx, "pdftohtml", args...)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("extract positioned PDF text: %w", err)
	}
	if len(xmlData) == 0 {
		return nil, nil, textsource.ErrEmptySource
	}
	pages, err := parseLayoutXML(ctx, xmlData)
	if err != nil {
		return nil, nil, fmt.Errorf("read PDF text layout: %w", err)
	}
	imageList, err := run(ctx, "pdfimages", "-list", path)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("inspect PDF page images: %w", err)
	}
	images, err := parseImageList(imageList)
	if err != nil {
		return nil, nil, fmt.Errorf("read PDF image listing: %w", err)
	}
	return pages, images, nil
}

func inspectOCRTools(ctx context.Context, run commandRunner, languages string) (string, error) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return "", fmt.Errorf("%w: install Poppler utilities with pdftoppm and try again", ErrOCRUnavailable)
	}
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "", fmt.Errorf("%w: install Tesseract OCR and its trained language data", ErrOCRUnavailable)
	}
	version, err := run(ctx, "tesseract", "--version")
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: could not start Tesseract", ErrOCRUnavailable)
	}
	versionLine := strings.TrimSpace(strings.SplitN(string(version), "\n", 2)[0])
	if versionLine == "" || len(versionLine) > 128 {
		return "", fmt.Errorf("%w: Tesseract returned an invalid version", ErrOCRUnavailable)
	}
	available, err := run(ctx, "tesseract", "--list-langs")
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: could not list installed language data", ErrOCRUnavailable)
	}
	installed := make(map[string]bool)
	for _, line := range strings.Split(string(available), "\n") {
		language := strings.TrimSpace(line)
		if language == "" || strings.HasPrefix(language, "List of available languages") {
			continue
		}
		installed[language] = true
	}
	for _, language := range strings.Split(languages, "+") {
		if !installed[language] {
			return "", fmt.Errorf("%w: %s (installed: %s)", ErrOCRLanguageUnavailable, language, strings.Join(sortedLanguageIDs(installed), ", "))
		}
	}
	return versionLine, nil
}

func sortedLanguageIDs(installed map[string]bool) []string {
	values := make([]string, 0, len(installed))
	for value := range installed {
		if value != "osd" {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}

func validateLanguageList(languages string) error {
	parts := strings.Split(languages, "+")
	if len(parts) == 0 || len(parts) > 8 {
		return errors.New("OCR languages must contain between one and eight Tesseract language IDs")
	}
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if part == "" || len(part) > 64 || seen[part] {
			return errors.New("OCR languages must be unique Tesseract IDs joined with '+', such as eng+por")
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
				return errors.New("OCR languages must be Tesseract IDs joined with '+', such as eng+por")
			}
		}
		seen[part] = true
	}
	return nil
}

func (b *PageSource) Close() {
	if b == nil || b.closed {
		return
	}
	b.closed = true
	_ = os.RemoveAll(b.outputDir)
	if b.cleanupInput != nil {
		b.cleanupInput()
	}
}

func (b *PageSource) DocumentID() string        { return b.documentID }
func (b *PageSource) Metadata() domain.Metadata { return b.metadata }
func (b *PageSource) PageCount() int            { return len(b.pages) }
func (b *PageSource) ScannedPageCount() int     { return b.scannedCount }

// CompleteDocument returns the complete document built by the last successful
// StreamPages call. It is unavailable after an interrupted or failed stream.
func (b *PageSource) CompleteDocument() (*domain.Document, bool) {
	if b == nil || b.complete == nil {
		return nil, false
	}
	return b.complete, true
}

func (b *PageSource) Parse(ctx context.Context) (*domain.Document, error) {
	if ctx == nil {
		return nil, errors.New("PDF parser requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.scannedCount == 0 {
		blocks := makeBlocks(ctx, b.pages, b.sourcePath)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return b.document(blocks, nil, nil)
	}
	if err := b.StreamPages(ctx, func(PageResult) error { return nil }); err != nil {
		return nil, err
	}
	if b.complete == nil {
		return nil, textsource.ErrEmptySource
	}
	return b.complete, nil
}

// StreamPages emits page-local IR in PDF order. Each page is OCRed and cached
// before it is emitted, so downstream segmentation and speech can begin while
// later scanned pages are still being recognized.
func (b *PageSource) StreamPages(ctx context.Context, emit func(PageResult) error) error {
	if ctx == nil {
		return errors.New("PDF page stream requires a context")
	}
	if emit == nil {
		return errors.New("PDF page stream requires a page receiver")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.pageProgress = 0
	b.complete = nil
	var precedingBlocks []domain.Block
	var lowConfidence, emptyOCR []int
	for index, original := range b.pages {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := b.pageResult(ctx, original, b.scanned[index])
		if err != nil {
			return err
		}
		if result.Warning != "" {
			if strings.Contains(result.Warning, "no readable words") {
				emptyOCR = append(emptyOCR, result.Page)
			} else {
				lowConfidence = append(lowConfidence, result.Page)
			}
		}
		start := uint64(len(precedingBlocks))
		globalPageBlocks := append([]domain.Block(nil), result.Document.Blocks...)
		for blockIndex := range globalPageBlocks {
			globalPageBlocks[blockIndex].Seq = start + uint64(blockIndex)
		}
		precedingBlocks = append(precedingBlocks, globalPageBlocks...)
		end := uint64(len(precedingBlocks))
		outline := b.outlineForBlocks(precedingBlocks)
		result.Document.Outline = localizeOutline(projectOutline(outline, start, end), start)
		if err := emit(result); err != nil {
			return err
		}
	}
	document, err := b.document(precedingBlocks, lowConfidence, emptyOCR)
	if err != nil {
		return err
	}
	b.complete = document
	return nil
}

func localizeOutline(nodes []*domain.OutlineNode, offset uint64) []*domain.OutlineNode {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		node.StartBlock -= offset
		node.EndBlockExclusive -= offset
		node.Children = localizeOutline(node.Children, offset)
	}
	return nodes
}

func (b *PageSource) outlineForBlocks(blocks []domain.Block) []*domain.OutlineNode {
	if len(b.bookmarks) > 0 {
		if nodes := outlineFromBookmarks(b.bookmarks, blocks, b.sourcePath, b.documentID); len(nodes) > 0 {
			return nodes
		}
	}
	return outlineFromHeadings(blocks, b.sourcePath, b.documentID)
}

func projectOutline(nodes []*domain.OutlineNode, start, end uint64) []*domain.OutlineNode {
	var result []*domain.OutlineNode
	for _, node := range nodes {
		if node == nil || node.EndBlockExclusive <= start || node.StartBlock >= end {
			continue
		}
		copyNode := *node
		if copyNode.StartBlock < start {
			copyNode.StartBlock = start
		}
		if copyNode.EndBlockExclusive > end {
			copyNode.EndBlockExclusive = end
		}
		copyNode.Children = projectOutline(node.Children, start, end)
		result = append(result, &copyNode)
	}
	return result
}

func (b *PageSource) pageResult(ctx context.Context, original pageLayout, scanned bool) (PageResult, error) {
	page := original
	result := pageOCRResult{}
	if scanned {
		var err error
		result, err = b.recognizePage(ctx, page)
		if err != nil {
			return PageResult{}, err
		}
		page.lines = result.lines
		page.ocr = true
	}
	blocks := makeBlocks(ctx, []pageLayout{page}, b.sourcePath)
	if err := ctx.Err(); err != nil {
		return PageResult{}, err
	}
	document := domain.Document{ID: b.documentID, Format: domain.FormatPDF, Metadata: b.metadata, Blocks: blocks}
	document.Outline = outlineFromHeadings(blocks, b.sourcePath, b.documentID)
	pageResult := PageResult{Document: document, Page: page.number, Scanned: scanned, Confidence: result.confidence}
	if scanned && result.words == 0 {
		pageResult.Warning = fmt.Sprintf("OCR found no readable words on PDF page %d.", page.number)
	} else if scanned && result.confidence < minOCRConfidence {
		pageResult.Warning = fmt.Sprintf("OCR confidence is low on PDF page %d; some words may be wrong.", page.number)
	}
	return pageResult, nil
}

func (b *PageSource) document(blocks []domain.Block, lowConfidence, emptyOCR []int) (*domain.Document, error) {
	if len(blocks) == 0 {
		return nil, textsource.ErrEmptySource
	}
	doc := &domain.Document{ID: b.documentID, Format: domain.FormatPDF, Metadata: b.metadata, Blocks: blocks}
	addWarning(&doc.Warnings, "PDF text and reading order were reconstructed from page geometry; complex layouts may differ from the source.")
	if b.tagged {
		addWarning(&doc.Warnings, "This PDF contains tagged structure that was not used; bookmarks and page layout determined its reading order.")
	}
	if b.scannedCount > 0 {
		addWarning(&doc.Warnings, "Scanned pages were recognized with local OCR. Recognition can misread text, especially small or degraded print.")
	}
	for _, page := range lowConfidence {
		addWarning(&doc.Warnings, fmt.Sprintf("OCR confidence is low on PDF page %d; some words may be wrong.", page))
	}
	for _, page := range emptyOCR {
		addWarning(&doc.Warnings, fmt.Sprintf("OCR found no readable words on PDF page %d.", page))
	}
	if len(b.bookmarks) > 0 {
		doc.Outline = outlineFromBookmarks(b.bookmarks, doc.Blocks, b.sourcePath, b.documentID)
		if len(doc.Outline) == 0 {
			addWarning(&doc.Warnings, "PDF bookmarks could not be matched to readable pages; visual layout was used instead.")
		}
	}
	if len(doc.Outline) == 0 {
		doc.Outline = outlineFromHeadings(doc.Blocks, b.sourcePath, b.documentID)
		if b.bookmarkNotes != "" {
			addWarning(&doc.Warnings, b.bookmarkNotes)
		}
		if len(doc.Outline) == 0 {
			addWarning(&doc.Warnings, "No reliable section headings were found in this PDF; reading order follows page layout.")
		}
	}
	return doc, nil
}

type pageOCRResult struct {
	lines      []pdfLine
	confidence float64
	words      int
	cacheHit   bool
}

func (b *PageSource) recognizePage(ctx context.Context, page pageLayout) (pageOCRResult, error) {
	key := cache.Key(ocrCacheVersion, parserVersion, b.documentID, strconv.Itoa(page.number), b.engineVersion, b.config.Languages, strconv.Itoa(b.config.DPI))
	var artifact ocrArtifact
	cacheHit := false
	if b.config.Cache != nil {
		found, err := b.config.Cache.Get(ctx, cache.StageOCR, key, &artifact)
		if err != nil {
			return pageOCRResult{}, fmt.Errorf("read cached OCR for PDF page %d: %w", page.number, err)
		}
		cacheHit = found
	}
	if !cacheHit {
		imagePath, err := b.renderPage(ctx, page.number)
		if err != nil {
			return pageOCRResult{}, err
		}
		defer os.Remove(imagePath)
		tsv, err := b.run(ctx, "tesseract", imagePath, "stdout", "-l", b.config.Languages, "--psm", "3", "tsv")
		if err != nil {
			if ctx.Err() != nil {
				return pageOCRResult{}, ctx.Err()
			}
			return pageOCRResult{}, fmt.Errorf("%w on PDF page %d: %v", ErrOCRFailed, page.number, err)
		}
		if len(tsv) > maxOCRTSVBytes {
			return pageOCRResult{}, fmt.Errorf("%w: Tesseract output for PDF page %d is too large", ErrResourceLimit, page.number)
		}
		artifact = ocrArtifact{Version: ocrCacheVersion, TSV: string(tsv)}
		if b.config.Cache != nil {
			if err := b.config.Cache.PutAtomic(ctx, cache.StageOCR, key, artifact); err != nil {
				return pageOCRResult{}, fmt.Errorf("save OCR for PDF page %d: %w", page.number, err)
			}
		}
	}
	if artifact.Version != ocrCacheVersion {
		return pageOCRResult{}, fmt.Errorf("cached OCR for PDF page %d uses an unsupported format", page.number)
	}
	lines, confidence, words, err := parseOCRTSV(ctx, []byte(artifact.TSV), page.number, page.width/float64(b.config.DPI), page.height/float64(b.config.DPI))
	if err != nil {
		return pageOCRResult{}, fmt.Errorf("read OCR output for PDF page %d: %w", page.number, err)
	}
	b.pageProgress++
	if b.config.OnProgress != nil {
		b.config.OnProgress(OCRProgress{
			Page: page.number, TotalPages: len(b.pages), ScannedComplete: b.pageProgress,
			ScannedTotal: b.scannedCount, CacheHit: cacheHit,
		})
	}
	return pageOCRResult{lines: lines, confidence: confidence, words: words, cacheHit: cacheHit}, nil
}

func (b *PageSource) renderPage(ctx context.Context, page int) (string, error) {
	prefix := filepath.Join(b.outputDir, fmt.Sprintf("ocr-page-%05d", page))
	args := []string{"-q", "-f", strconv.Itoa(page), "-l", strconv.Itoa(page), "-singlefile", "-r", strconv.Itoa(b.config.DPI), "-png", b.path, prefix}
	if _, err := b.runDir(ctx, b.outputDir, "pdftoppm", args...); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("render PDF page %d for OCR: %w", page, err)
	}
	imagePath := prefix + ".png"
	info, err := os.Stat(imagePath)
	if err != nil {
		return "", fmt.Errorf("render PDF page %d for OCR: expected raster image was not created: %w", page, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 128<<20 {
		_ = os.Remove(imagePath)
		return "", fmt.Errorf("%w: raster for PDF page %d exceeds the supported size", ErrResourceLimit, page)
	}
	return imagePath, nil
}

func parseOCRTSV(ctx context.Context, data []byte, pageNumber int, scaleX, scaleY float64) ([]pdfLine, float64, int, error) {
	if len(data) == 0 {
		return nil, 0, 0, errors.New("Tesseract returned no tab-separated output")
	}
	if !isFinitePositive(scaleX) || !isFinitePositive(scaleY) {
		return nil, 0, 0, errors.New("PDF page dimensions are unavailable for OCR layout")
	}
	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, 0, 0, err
	}
	if len(records) < 2 {
		return nil, 0, 0, errors.New("Tesseract returned no word rows")
	}
	columns := make(map[string]int, len(records[0]))
	for index, name := range records[0] {
		columns[strings.TrimSpace(name)] = index
	}
	for _, name := range []string{"level", "block_num", "par_num", "line_num", "left", "top", "width", "height", "conf", "text"} {
		if _, ok := columns[name]; !ok {
			return nil, 0, 0, fmt.Errorf("Tesseract TSV is missing the %q column", name)
		}
	}
	get := func(row []string, name string) string {
		index := columns[name]
		if index >= len(row) {
			return ""
		}
		return row[index]
	}
	type lineKey struct{ block, paragraph, line string }
	lineIndexes := make(map[lineKey]int)
	lines := make([]pdfLine, 0)
	confidenceTotal := 0.0
	words := 0
	for _, row := range records[1:] {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, err
		}
		if len(row) < len(records[0]) {
			continue
		}
		level, _ := strconv.Atoi(get(row, "level"))
		text := normalizeText(get(row, "text"))
		if level != 5 || text == "" {
			continue
		}
		left, leftErr := strconv.ParseFloat(get(row, "left"), 64)
		top, topErr := strconv.ParseFloat(get(row, "top"), 64)
		width, widthErr := strconv.ParseFloat(get(row, "width"), 64)
		height, heightErr := strconv.ParseFloat(get(row, "height"), 64)
		confidence, confidenceErr := strconv.ParseFloat(get(row, "conf"), 64)
		if leftErr != nil || topErr != nil || widthErr != nil || heightErr != nil || confidenceErr != nil ||
			left < 0 || top < 0 || width < 0 || height < 0 || math.IsNaN(confidence) || math.IsInf(confidence, 0) {
			return nil, 0, 0, errors.New("Tesseract returned invalid word geometry or confidence")
		}
		key := lineKey{block: get(row, "block_num"), paragraph: get(row, "par_num"), line: get(row, "line_num")}
		lineIndex, ok := lineIndexes[key]
		if !ok {
			lineIndex = len(lines)
			lineIndexes[key] = lineIndex
			lines = append(lines, pdfLine{
				left: left * scaleX, top: top * scaleY,
				right: (left + width) * scaleX, bottom: (top + height) * scaleY,
				fontSize: math.Max(6, height*scaleY), family: "OCR", page: pageNumber,
			})
		} else {
			line := &lines[lineIndex]
			line.left = math.Min(line.left, left*scaleX)
			line.top = math.Min(line.top, top*scaleY)
			line.right = math.Max(line.right, (left+width)*scaleX)
			line.bottom = math.Max(line.bottom, (top+height)*scaleY)
			line.fontSize = math.Max(line.fontSize, height*scaleY)
		}
		if lines[lineIndex].text != "" {
			lines[lineIndex].text += " "
		}
		lines[lineIndex].text += text
		confidenceTotal += math.Max(0, math.Min(confidence, 100))
		words++
	}
	if words == 0 {
		return nil, 0, 0, nil
	}
	for index := range lines {
		lines[index].text = normalizeText(lines[index].text)
	}
	return lines, confidenceTotal / float64(words), words, nil
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
