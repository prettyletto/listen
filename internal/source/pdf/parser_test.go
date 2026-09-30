package pdf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prettyletto/listen/internal/cache"
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/source"
)

type goldenBlock struct {
	Seq   uint64           `json:"seq"`
	Kind  domain.BlockKind `json:"kind"`
	Text  string           `json:"text"`
	Level int              `json:"level,omitempty"`
	Page  int              `json:"page"`
}

type goldenDocument struct {
	Format domain.Format `json:"format"`
	Blocks []goldenBlock `json:"blocks"`
}

func TestOCRLanguageListValidation(t *testing.T) {
	for _, languages := range []string{"eng", "eng+por", "chi_sim+chi_tra"} {
		if err := validateLanguageList(languages); err != nil {
			t.Errorf("validateLanguageList(%q): %v", languages, err)
		}
	}
	for _, languages := range []string{"", "eng+", "+eng", "eng+eng", "eng por", strings.Repeat("a", 65), "a+b+c+d+e+f+g+h+i"} {
		if err := validateLanguageList(languages); err == nil {
			t.Errorf("validateLanguageList(%q) accepted an invalid language list", languages)
		}
	}
}

func TestPDFBornDigitalFixtureMatchesGolden(t *testing.T) {
	requirePDFTools(t)
	path := filepath.Join("..", "..", "..", "testdata", "pdf", "born-digital.pdf")
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "born-digital.pdf", Path: path})
	if err != nil {
		t.Fatalf("parse born-digital PDF: %v", err)
	}
	expectedTitle := "born-digital"
	if _, err := exec.LookPath("pdfinfo"); err == nil {
		expectedTitle = "The Sample Book"
	}
	if doc.Metadata.Title != expectedTitle {
		t.Fatalf("PDF title = %q, want %q", doc.Metadata.Title, expectedTitle)
	}
	actual, err := json.MarshalIndent(goldenFrom(doc), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	goldenPath := filepath.Join("..", "..", "..", "testdata", "expected", "pdf", "born-digital.json")
	if os.Getenv("UPDATE_PDF_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %q: %v (set UPDATE_PDF_GOLDEN=1 after reviewing the parser output)", goldenPath, err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("parsed PDF differs from %s; set UPDATE_PDF_GOLDEN=1 only after reviewing the parser change", goldenPath)
	}
	if !rangeInvariants(doc.Outline, uint64(len(doc.Blocks))) {
		t.Fatalf("PDF outline ranges are invalid: %#v", doc.Outline)
	}
	if len(doc.Warnings) == 0 || !strings.Contains(strings.Join(doc.Warnings, " "), "page geometry") {
		t.Fatalf("PDF did not report its best-effort layout confidence: %#v", doc.Warnings)
	}
}

func TestPDFBookmarksBecomeOutlineNodes(t *testing.T) {
	requirePDFTools(t)
	if _, err := exec.LookPath("qpdf"); err != nil {
		t.Skip("qpdf is not installed")
	}
	path := filepath.Join("..", "..", "..", "testdata", "pdf", "born-digital.pdf")
	if _, err := runCommand(context.Background(), "qpdf", "--json", "--json-key=outlines", path); err != nil {
		t.Skipf("qpdf does not support JSON outlines: %v", err)
	}
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "born-digital.pdf", Path: path})
	if err != nil {
		t.Fatalf("parse bookmarked PDF: %v", err)
	}
	wantTitles := []string{"Chapter 1: The Beginning", "A Useful Detail", "Chapter 2: The Middle", "Appendix: Afterword"}
	if len(doc.Outline) != len(wantTitles) {
		t.Fatalf("outline has %d nodes, want %d: %#v", len(doc.Outline), len(wantTitles), doc.Outline)
	}
	for i, node := range doc.Outline {
		if node.Title != wantTitles[i] || node.Provenance != domain.StructurePDFBookmark || node.Confidence != domain.ConfidenceHigh {
			t.Errorf("outline[%d] = %#v, want bookmark %q with high confidence", i, node, wantTitles[i])
		}
	}
	if doc.Outline[2].SourceTarget.Page != 2 || doc.Outline[3].SourceTarget.Page != 3 {
		t.Fatalf("bookmark pages were not preserved: %#v", doc.Outline)
	}
}

func TestNestedPDFBookmarksKeepContainedBlockRanges(t *testing.T) {
	page1, page2, page3 := 1, 2, 3
	blocks := []domain.Block{
		{ID: "a", Source: domain.SourceRef{Page: 1}},
		{ID: "b", Source: domain.SourceRef{Page: 1}},
		{ID: "c", Source: domain.SourceRef{Page: 2}},
		{ID: "d", Source: domain.SourceRef{Page: 3}},
	}
	bookmarks := []qpdfOutline{
		{Title: "Part I", DestinationPage: &page1, Children: []qpdfOutline{
			{Title: "Chapter 1", DestinationPage: &page1},
			{Title: "Chapter 2", DestinationPage: &page2},
		}},
		{Title: "Part II", DestinationPage: &page3},
	}
	nodes := outlineFromBookmarks(bookmarks, blocks, "book.pdf", "doc")
	if len(nodes) != 2 || len(nodes[0].Children) != 2 {
		t.Fatalf("nested bookmarks were not preserved: %#v", nodes)
	}
	if nodes[0].StartBlock != 0 || nodes[0].EndBlockExclusive != 3 || nodes[0].Children[0].EndBlockExclusive != 2 || nodes[0].Children[1].StartBlock != 2 {
		t.Fatalf("bookmark ranges do not follow their destination pages: %#v", nodes)
	}
	if !rangeInvariants(nodes, uint64(len(blocks))) {
		t.Fatalf("nested bookmark ranges are invalid: %#v", nodes)
	}
}

func TestScannedPDFFixtureIsOCRedInPageOrder(t *testing.T) {
	requirePDFTools(t)
	requireTesseract(t)
	path := filepath.Join("..", "..", "..", "testdata", "pdf", "scanned.pdf")
	imagesBefore, err := filepath.Glob(filepath.Join(filepath.Dir(path), "scanned-*.png"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "scanned.pdf", Path: path})
	if err != nil {
		t.Fatalf("parse scanned PDF with local OCR: %v", err)
	}
	var text []string
	for _, block := range doc.Blocks {
		text = append(text, block.Text)
	}
	joined := strings.Join(text, " ")
	if !strings.Contains(joined, "Page one OCR is local and readable.") || !strings.Contains(joined, "Page two follows page one in order.") {
		t.Fatalf("recognized page text was missing or out of order: %q", joined)
	}
	if doc.Blocks[0].Source.Page != 1 || doc.Blocks[len(doc.Blocks)-1].Source.Page != 2 {
		t.Fatalf("OCR blocks lost their source page references: %#v", doc.Blocks)
	}
	if !strings.Contains(strings.Join(doc.Warnings, " "), "recognized with local OCR") {
		t.Fatalf("document did not explain OCR uncertainty: %#v", doc.Warnings)
	}
	actual, err := json.MarshalIndent(goldenFrom(doc), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	goldenPath := filepath.Join("..", "..", "..", "testdata", "expected", "pdf", "scanned.json")
	if os.Getenv("UPDATE_PDF_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read scanned-PDF golden %q: %v (set UPDATE_PDF_GOLDEN=1 after reviewing the parser output)", goldenPath, err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("parsed scanned PDF differs from %s; set UPDATE_PDF_GOLDEN=1 only after reviewing the OCR output", goldenPath)
	}
	book, err := NewParser().Prepare(context.Background(), source.Input{Name: "scanned.pdf", Path: path}, OCRConfig{})
	if err != nil {
		t.Fatalf("prepare scanned PDF page stream: %v", err)
	}
	var streamed []segment.Segment
	err = book.StreamPages(context.Background(), func(page PageResult) error {
		for index, block := range page.Document.Blocks {
			if block.Seq != uint64(index) {
				return errors.New("page document block sequences are not page-local")
			}
		}
		if !rangeInvariants(page.Document.Outline, uint64(len(page.Document.Blocks))) {
			return errors.New("page document outline ranges are not page-local")
		}
		pagePlan, err := segment.PlanDocument(&page.Document, segment.Options{})
		if err != nil {
			return err
		}
		for _, item := range pagePlan.Segments {
			item.Seq = uint64(len(streamed))
			streamed = append(streamed, item)
		}
		return nil
	})
	book.Close()
	if err != nil {
		t.Fatalf("stream scanned PDF segments: %v", err)
	}
	fullPlan, err := segment.PlanDocument(doc, segment.Options{})
	if err != nil {
		t.Fatalf("plan complete scanned PDF: %v", err)
	}
	if len(streamed) != len(fullPlan.Segments) {
		t.Fatalf("streamed segment count = %d, complete plan count = %d", len(streamed), len(fullPlan.Segments))
	}
	for index := range streamed {
		if streamed[index].ID != fullPlan.Segments[index].ID || streamed[index].Seq != fullPlan.Segments[index].Seq || streamed[index].SourceText != fullPlan.Segments[index].SourceText || streamed[index].MajorUnitID != fullPlan.Segments[index].MajorUnitID || !reflect.DeepEqual(streamed[index].SectionPath, fullPlan.Segments[index].SectionPath) {
			t.Fatalf("page-stream segment %d differs from the complete PDF plan:\nstreamed=%#v\ncomplete=%#v", index, streamed[index], fullPlan.Segments[index])
		}
	}
	imagesAfter, err := filepath.Glob(filepath.Join(filepath.Dir(path), "scanned-*.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(imagesAfter, imagesBefore) {
		t.Fatalf("PDF parsing left extracted images beside the source: %#v", imagesAfter)
	}
}

func TestScannedPDFPageStreamUsesAtomicPageOCRCache(t *testing.T) {
	requirePDFTools(t)
	requireTesseract(t)
	path := filepath.Join("..", "..", "..", "testdata", "pdf", "scanned.pdf")
	store, err := cache.NewFileStore(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("test interruption after first OCR page")
	for pass := 0; pass < 3; pass++ {
		var progress []OCRProgress
		book, err := NewParser().Prepare(context.Background(), source.Input{Name: "scanned.pdf", Path: path}, OCRConfig{
			Cache: store, Languages: "eng", OnProgress: func(value OCRProgress) { progress = append(progress, value) },
		})
		if err != nil {
			t.Fatalf("prepare scanned PDF: %v", err)
		}
		var pages []PageResult
		err = book.StreamPages(context.Background(), func(page PageResult) error {
			pages = append(pages, page)
			if pass == 0 {
				return interrupted
			}
			return nil
		})
		complete, hasComplete := book.CompleteDocument()
		book.Close()
		if pass == 0 && !errors.Is(err, interrupted) {
			t.Fatalf("partial OCR pass error = %v, want injected interruption", err)
		}
		if pass > 0 && err != nil {
			t.Fatalf("stream scanned PDF pages: %v", err)
		}
		if hasComplete != (pass > 0) || (hasComplete && len(complete.Blocks) == 0) {
			t.Fatalf("complete document availability = %v, document=%#v after pass %d", hasComplete, complete, pass)
		}
		wantPages := 2
		if pass == 0 {
			wantPages = 1
		}
		if len(pages) != wantPages || pages[0].Page != 1 || (wantPages == 2 && pages[1].Page != 2) {
			t.Fatalf("streamed pages = %#v, want source order 1, 2", pages)
		}
		if len(progress) != wantPages {
			t.Fatalf("OCR progress = %#v, want %d completed pages", progress, wantPages)
		}
		if pass == 0 && progress[0].CacheHit {
			t.Fatalf("first OCR pass unexpectedly hit page cache: %#v", progress)
		}
		if pass == 1 && (!progress[0].CacheHit || progress[1].CacheHit) {
			t.Fatalf("resumed OCR did not reuse completed first page only: %#v", progress)
		}
		if pass == 2 && (!progress[0].CacheHit || !progress[1].CacheHit) {
			t.Fatalf("second OCR pass did not reuse completed page results: %#v", progress)
		}
	}
}

func requireTesseract(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("Tesseract OCR is not installed")
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("Poppler pdftoppm is not installed")
	}
}

func TestPDFParserProbeAndInputReaderAreStable(t *testing.T) {
	parser := NewParser()
	data := []byte("%PDF-1.4\nfixture")
	reader := bytes.NewReader(data)
	score, err := parser.Probe(context.Background(), source.Input{Name: "unknown", R: reader})
	if err != nil || score == 0 {
		t.Fatalf("PDF signature probe = %d, %v", score, err)
	}
	position, err := reader.Seek(0, 1)
	if err != nil || position != 0 {
		t.Fatalf("probe consumed the input reader: position=%d error=%v", position, err)
	}
	if score, err := parser.Probe(context.Background(), source.Input{Name: "book.PDF"}); err != nil || score != 100 {
		t.Fatalf("extension probe = %d, %v", score, err)
	}
}

func TestPDFReaderInputUsesAndCleansTemporaryFile(t *testing.T) {
	data := []byte("%PDF-1.4\nfixture")
	path, cleanup, err := materializeInput(context.Background(), source.Input{Name: "book.pdf", R: bytes.NewReader(data)})
	if err != nil {
		t.Fatalf("materialize PDF reader: %v", err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("temporary PDF content = %q, %v", actual, err)
	}
	cleanup()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary PDF was not removed: %v", err)
	}
}

func TestPDFResourceLimitsRejectOversizedInput(t *testing.T) {
	if err := checkPDFSize(maxPDFBytes); err != nil {
		t.Fatalf("size at PDF limit rejected: %v", err)
	}
	if err := checkPDFSize(maxPDFBytes + 1); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversized PDF error = %v, want ErrResourceLimit", err)
	}
}

func TestPDFReaderInputStopsAtSizeLimit(t *testing.T) {
	var dst bytes.Buffer
	_, err := copyAtMost(context.Background(), &dst, bytes.NewReader([]byte("12345")), 4)
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversized reader error = %v, want ErrResourceLimit", err)
	}
	if dst.String() != "12345" {
		t.Fatalf("reader copied %q, want five bytes before rejecting the input", dst.String())
	}
}

func TestPDFImageListingEstimatesPageCoverage(t *testing.T) {
	listing := []byte("page num type width height color comp bpc enc interp object ID x-ppi y-ppi size ratio\n" +
		"   1   0 image 2550 3300 rgb 3 8 jpeg no 25 0 300 300 436K 2.1%\n" +
		"   2   0 image 100 200 gray 1 8 image no [inline] 72 72 1B 100%\n")
	images, err := parseImageList(listing)
	if err != nil {
		t.Fatalf("parse image listing: %v", err)
	}
	if len(images[1]) != 1 || math.Abs(images[1][0].width-612) > 0.001 || math.Abs(images[1][0].height-792) > 0.001 {
		t.Fatalf("first page image size = %#v, want 612 by 792 points", images[1])
	}
	if len(images[2]) != 1 || math.Abs(images[2][0].width-100) > 0.001 || math.Abs(images[2][0].height-200) > 0.001 {
		t.Fatalf("inline image size = %#v, want 100 by 200 points", images[2])
	}
	if len(images) != 2 {
		t.Fatalf("image pages = %#v, want two pages", images)
	}
}

func TestPDFImageListingRejectsImageCountOverflow(t *testing.T) {
	listing := []byte("1 0 image 2550 3300 rgb 3 8 jpeg no 25 0 300 300 436K 2.1%\n" +
		"2 0 image 2550 3300 rgb 3 8 jpeg no 26 0 300 300 436K 2.1%\n")
	if _, err := parseImageListing(listing, 1); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("image-count error = %v, want ErrResourceLimit", err)
	}
}

func TestPDFLayoutRejectsPageLimitDuringDecode(t *testing.T) {
	var input strings.Builder
	input.WriteString("<doc>")
	for page := 0; page <= maxPDFPages; page++ {
		input.WriteString(`<page number="1" width="600" height="800"></page>`)
	}
	input.WriteString("</doc>")
	_, err := parseLayoutXML(context.Background(), []byte(input.String()))
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("page-limit error = %v, want ErrResourceLimit", err)
	}
}

func TestPDFBookmarkNestingLimit(t *testing.T) {
	node := qpdfOutline{Title: "leaf"}
	for depth := 0; depth < maxPDFBookmarkDepth; depth++ {
		node = qpdfOutline{Title: "parent", Children: []qpdfOutline{node}}
	}
	if err := validateBookmarkLimits([]qpdfOutline{node}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("bookmark-depth error = %v, want ErrResourceLimit", err)
	}
}

func TestCappedBufferStopsSubprocessOutputAtLimit(t *testing.T) {
	buffer := &cappedBuffer{limit: 4}
	if written, err := buffer.Write([]byte("123456")); written != 4 || !errors.Is(err, errOutputLimitReached) {
		t.Fatalf("write = %d, %v; want 4 and output limit error", written, err)
	}
	if !buffer.exceeded || buffer.buffer.String() != "1234" {
		t.Fatalf("buffer state = exceeded %v, content %q", buffer.exceeded, buffer.buffer.String())
	}
}

func TestCanceledPDFParseReturnsContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewParser().Parse(ctx, source.Input{Name: "book.pdf", Path: "missing.pdf"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestPDFSubprocessStopsWhenContextExpires(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep command is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runCommand(ctx, "sleep", "10")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context deadline", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("child process was not stopped promptly after cancellation")
	}
}

func TestMarginSuppressionRemovesRepeatedHeadersAndPageNumbers(t *testing.T) {
	pages := []pageLayout{
		{number: 1, width: 600, height: 800, lines: []pdfLine{{text: "Running Book Title", top: 20, bottom: 30}, {text: "1", top: 770, bottom: 780}, {text: "Body", top: 200, bottom: 212}}},
		{number: 2, width: 600, height: 800, lines: []pdfLine{{text: "Running Book Title", top: 21, bottom: 31}, {text: "2", top: 770, bottom: 780}, {text: "More body", top: 200, bottom: 212}}},
	}
	suppressMargins(pages)
	for _, page := range pages {
		if len(page.lines) != 1 || page.lines[0].top < 100 {
			t.Fatalf("page margin text was retained: %#v", page.lines)
		}
	}
}

func TestColumnReconstructionReadsOneColumnAtATime(t *testing.T) {
	page := pageLayout{number: 1, width: 600, height: 800, lines: []pdfLine{
		{text: "Left one", left: 50, right: 245, top: 100, bottom: 112},
		{text: "Right one", left: 340, right: 535, top: 100, bottom: 112},
		{text: "Left two", left: 50, right: 245, top: 120, bottom: 132},
		{text: "Right two", left: 340, right: 535, top: 120, bottom: 132},
		{text: "Left three", left: 50, right: 245, top: 140, bottom: 152},
		{text: "Right three", left: 340, right: 535, top: 140, bottom: 152},
	}}
	got := orderedPageLines(page)
	var texts []string
	for _, line := range got {
		texts = append(texts, line.text)
	}
	want := []string{"Left one", "Left two", "Left three", "Right one", "Right two", "Right three"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("column reading order = %#v, want %#v", texts, want)
	}
}

func TestTextFragmentGroupingPreservesNarrowColumnGutter(t *testing.T) {
	page := pageLayout{number: 1, width: 600, height: 800, raw: []rawText{
		{text: "A", left: 50, top: 100, width: 6, height: 10, fontSize: 11},
		{text: "word", left: 60, top: 100, width: 28, height: 10, fontSize: 11},
		{text: "Right column", left: 258, top: 100, width: 80, height: 10, fontSize: 11},
	}}
	lines := groupTextLines(page)
	if len(lines) != 2 || lines[0].text != "A word" || lines[1].text != "Right column" {
		t.Fatalf("PDF text fragments = %#v, want separate columns and joined words", lines)
	}
}

func TestPDFGeometrySortHandlesStaggeredBaselinesDeterministically(t *testing.T) {
	page := pageLayout{number: 1, width: 600, raw: []rawText{
		{text: "first", left: 100, top: 0, width: 10, height: 10, fontSize: 10},
		{text: "second", left: 50, top: 1.5, width: 10, height: 10, fontSize: 10},
		{text: "third", left: 0, top: 3, width: 10, height: 10, fontSize: 10},
	}}
	grouped := groupTextLines(page)
	if len(grouped) != 3 || grouped[0].text != "first" || grouped[1].text != "second" || grouped[2].text != "third" {
		t.Fatalf("staggered baseline order = %#v", grouped)
	}

	lines := []pdfLine{
		{text: "first", left: 100, top: 0},
		{text: "second", left: 50, top: 1.5},
		{text: "third", left: 0, top: 3},
	}
	sortPageLines(lines)
	if lines[0].text != "first" || lines[1].text != "second" || lines[2].text != "third" {
		t.Fatalf("page line order = %#v", lines)
	}
}

func TestVisualHeadingClassifierKeepsStyledQuestionHeadings(t *testing.T) {
	page := pageLayout{width: 600, height: 800}
	styled := pdfLine{text: "Where Did It Go?", left: 210, right: 390, top: 100, bottom: 118, fontSize: 18, bold: true}
	if !isVisualHeading(page, styled, 12, 14, 14, map[string]map[int]bool{}) {
		t.Fatal("a large, bold question-style title was not classified as a heading")
	}
	plain := pdfLine{text: "Where did it go?", left: 50, right: 160, top: 100, bottom: 112, fontSize: 12}
	if isVisualHeading(page, plain, 12, 0, 0, map[string]map[int]bool{}) {
		t.Fatal("ordinary question prose was classified as a heading")
	}
}

func goldenFrom(doc *domain.Document) goldenDocument {
	result := goldenDocument{Format: doc.Format}
	for _, block := range doc.Blocks {
		result.Blocks = append(result.Blocks, goldenBlock{Seq: block.Seq, Kind: block.Kind, Text: block.Text, Level: block.Level, Page: block.Source.Page})
	}
	return result
}

func requirePDFTools(t *testing.T) {
	t.Helper()
	for _, name := range []string{"pdftohtml", "pdfimages"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("Poppler PDF tools are not installed")
		}
	}
}

func rangeInvariants(nodes []*domain.OutlineNode, blockCount uint64) bool {
	for _, node := range nodes {
		if node.StartBlock >= node.EndBlockExclusive || node.EndBlockExclusive > blockCount {
			return false
		}
		for _, child := range node.Children {
			if child.StartBlock < node.StartBlock || child.EndBlockExclusive > node.EndBlockExclusive {
				return false
			}
		}
		if !rangeInvariants(node.Children, blockCount) {
			return false
		}
	}
	return true
}

func TestMarginKeyNormalizesLabels(t *testing.T) {
	if got := marginKey("THE SAMPLE BOOK — 02"); got != "the sample book" {
		t.Fatalf("marginKey = %q", got)
	}
	if strings.Contains(marginKey("Running Book Title"), "  ") {
		t.Fatal("margin key contains duplicate whitespace")
	}
}
