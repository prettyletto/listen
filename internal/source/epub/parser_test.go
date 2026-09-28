package epub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/source"
)

var epubFixtures = []string{
	"nested",
	"split_chapter",
	"multi_chapter_one_file",
	"ncx_fallback",
	"broken_fragment",
	"nonlinear_navigation",
	"broken_navigation",
}

func TestEPUBFixturesMatchGolden(t *testing.T) {
	for _, name := range epubFixtures {
		t.Run(name, func(t *testing.T) {
			document, err := NewParser().Parse(context.Background(), fixtureInput(name))
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			actual, err := json.MarshalIndent(document, "", "  ")
			if err != nil {
				t.Fatalf("marshal parsed document: %v", err)
			}
			actual = append(actual, '\n')
			goldenPath := filepath.Join("..", "..", "..", "testdata", "expected", "epub", name+".json")
			if os.Getenv("UPDATE_EPUB_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatalf("create golden directory: %v", err)
				}
				if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			expected, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %q: %v (set UPDATE_EPUB_GOLDEN=1 to regenerate)", goldenPath, err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatalf("parsed document differs from %s; set UPDATE_EPUB_GOLDEN=1 only after reviewing the parser change", goldenPath)
			}
		})
	}
}

func TestNestedFixtureRetainsTOCAndFootnoteButSkipsDefaultNarration(t *testing.T) {
	document := mustParseFixture(t, "nested")
	var tocBlocks, notes int
	for _, block := range document.Blocks {
		if block.Source.Path == "OEBPS/nav.xhtml" {
			if !block.SkipByDefault {
				t.Errorf("navigation block %q is not skipped by default", block.Text)
			}
			tocBlocks++
		}
		if block.Kind == domain.BlockNote {
			notes++
			if !block.SkipByDefault || !contains(block.Semantics, "footnote") {
				t.Errorf("footnote block should be retained as a skipped footnote: %#v", block)
			}
		}
	}
	if tocBlocks == 0 {
		t.Fatal("fixture should retain its in-spine navigation page")
	}
	if notes != 1 {
		t.Fatalf("got %d note blocks, want 1", notes)
	}
	part := findOutline(document.Outline, "Part I")
	if part == nil || part.Role != domain.RolePart {
		t.Fatalf("Part I role was not preserved: %#v", part)
	}
	chapter := findOutline(part.Children, "Chapter 1")
	section := findOutline(chapter.Children, "Section 1.1")
	if chapter == nil || chapter.Role != domain.RoleChapter || section == nil || section.Role != domain.RoleSection {
		t.Fatalf("nested EPUB hierarchy was not preserved: chapter=%#v section=%#v", chapter, section)
	}
}

func TestSplitChapterRangeSpansSpineFiles(t *testing.T) {
	document := mustParseFixture(t, "split_chapter")
	chapter := findOutline(document.Outline, "Chapter 1")
	if chapter == nil {
		t.Fatal("Chapter 1 was not mapped")
	}
	paths := map[string]bool{}
	for seq := chapter.StartBlock; seq < chapter.EndBlockExclusive; seq++ {
		paths[document.Blocks[seq].Source.Path] = true
	}
	if len(paths) != 2 {
		t.Fatalf("Chapter 1 range touches %d source files, want two: %#v", len(paths), paths)
	}
}

func TestOneXHTMLFileCanContainSeparateChapterRanges(t *testing.T) {
	document := mustParseFixture(t, "multi_chapter_one_file")
	first := findOutline(document.Outline, "Chapter 1")
	second := findOutline(document.Outline, "Chapter 2")
	if first == nil || second == nil {
		t.Fatalf("chapters were not mapped: first=%#v second=%#v", first, second)
	}
	if first.SourceTarget.Path != second.SourceTarget.Path {
		t.Fatalf("chapters should target one XHTML resource: %q / %q", first.SourceTarget.Path, second.SourceTarget.Path)
	}
	if first.EndBlockExclusive != second.StartBlock || first.StartBlock >= second.StartBlock {
		t.Fatalf("chapter ranges do not split at the second fragment: first=%#v second=%#v", first, second)
	}
}

func TestNCXFallsBackWithHighConfidenceStructure(t *testing.T) {
	document := mustParseFixture(t, "ncx_fallback")
	if document.Outline[0].Provenance != domain.StructureNCX {
		t.Fatalf("root provenance=%q, want NCX", document.Outline[0].Provenance)
	}
	if !warningContains(document.Warnings, "EPUB 2 NCX provided") {
		t.Fatalf("warnings do not explain NCX fallback: %#v", document.Warnings)
	}
	for _, child := range document.Outline[0].Children {
		if child.Provenance != domain.StructureNCX || child.Confidence != domain.ConfidenceHigh {
			t.Errorf("NCX node should retain high-confidence provenance: %#v", child)
		}
	}
}

func TestBrokenNavigationFragmentRepairsToSpineDocumentStart(t *testing.T) {
	document := mustParseFixture(t, "broken_fragment")
	chapter := findOutline(document.Outline, "Chapter One")
	if chapter == nil || chapter.StartBlock != 0 {
		t.Fatalf("broken fragment did not repair to the chapter document start: %#v", chapter)
	}
	if !warningContains(document.Warnings, "fragment \"missing-heading\" was missing") {
		t.Fatalf("missing fragment repair was not explained: %#v", document.Warnings)
	}
}

func TestNonlinearNavigationIsRetainedOutsideReadingBlocks(t *testing.T) {
	document := mustParseFixture(t, "nonlinear_navigation")
	if len(document.AuxiliaryNavigation) != 1 {
		t.Fatalf("got %d auxiliary navigation targets, want one: %#v", len(document.AuxiliaryNavigation), document.AuxiliaryNavigation)
	}
	target := document.AuxiliaryNavigation[0]
	if target.Title != "Supplement" || target.SourceTarget.Path != "OEBPS/supplement.xhtml" || target.SourceTarget.Fragment != "appendix" {
		t.Fatalf("auxiliary target metadata was lost: %#v", target)
	}
	if len(target.NavigationPath) != 1 || target.NavigationPath[0] != "Supplementary Material" {
		t.Fatalf("auxiliary target lost its navigation parent: %#v", target.NavigationPath)
	}
	if target.SourceTarget.SpineIndex != 1 || target.Provenance != domain.StructureEPUBNav {
		t.Fatalf("auxiliary target lost its spine index or provenance: %#v", target)
	}
	for _, block := range document.Blocks {
		if strings.Contains(block.Text, "supplement body") {
			t.Fatal("non-linear supplement content was inserted into canonical reading order")
		}
	}
	if !warningContains(document.Warnings, "Supplement") || !warningContains(document.Warnings, "outside the linear reading spine") {
		t.Fatalf("diagnostic does not explain the auxiliary navigation target: %#v", document.Warnings)
	}
}

func TestMissingNavigationResourceIsNotRetainedAsAuxiliary(t *testing.T) {
	document := mustParseFixture(t, "broken_navigation")
	if len(document.AuxiliaryNavigation) != 0 {
		t.Fatalf("missing resource was mislabeled auxiliary: %#v", document.AuxiliaryNavigation)
	}
	if !warningContains(document.Warnings, "absent from the package manifest") || !warningContains(document.Warnings, "linear spine resource with no readable blocks") {
		t.Fatalf("missing/unsupported resources were not diagnosed separately: %#v", document.Warnings)
	}
}

func TestNestedEPUBPlansSkipTOCAndKeepMajorUnitsSeparate(t *testing.T) {
	document := mustParseFixture(t, "nested")
	plan, err := segment.PlanDocument(document, segment.Options{TargetWords: 4, MaxWords: 12})
	if err != nil {
		t.Fatalf("plan EPUB: %v", err)
	}
	var chapterOneUnit, chapterTwoUnit string
	for _, item := range plan.Segments {
		if strings.Contains(strings.ToLower(item.SpokenText), "contents") {
			t.Fatalf("TOC text entered the default speech plan: %q", item.SpokenText)
		}
		switch item.Source.Path {
		case "OEBPS/chapter1.xhtml":
			chapterOneUnit = item.MajorUnitID
			if !contains(item.SectionPath, "Section 1.1") && !contains(item.SectionPath, "Section 1.2") && !contains(item.SectionPath, "Chapter 1") {
				t.Errorf("chapter segment lost its outline path: %#v", item)
			}
		case "OEBPS/chapter2.xhtml":
			chapterTwoUnit = item.MajorUnitID
		}
	}
	if chapterOneUnit == "" || chapterTwoUnit == "" || chapterOneUnit == chapterTwoUnit {
		t.Fatalf("chapter segments do not have distinct major units: chapter1=%q chapter2=%q", chapterOneUnit, chapterTwoUnit)
	}
}

func TestEPUBMajorUnitFallbackPromotesSubstantialPartChildren(t *testing.T) {
	blocks := []domain.Block{
		{Seq: 0, Kind: domain.BlockHeading, Text: "Opening one"},
		{Seq: 1, Kind: domain.BlockParagraph, Text: strings.Repeat("alpha ", 80)},
		{Seq: 2, Kind: domain.BlockHeading, Text: "Opening two"},
		{Seq: 3, Kind: domain.BlockParagraph, Text: strings.Repeat("beta ", 80)},
	}
	part := &domain.OutlineNode{
		Title: "Part I", Role: domain.RolePart, StartBlock: 0, EndBlockExclusive: 4,
		Children: []*domain.OutlineNode{
			{Title: "One", Role: domain.RoleUnknown, StartBlock: 0, EndBlockExclusive: 2},
			{Title: "Two", Role: domain.RoleUnknown, StartBlock: 2, EndBlockExclusive: 4},
		},
	}
	classifyMajorUnits(part, blocks)
	for _, child := range part.Children {
		if child.Role != domain.RoleChapter {
			t.Errorf("substantial child %q role=%q, want chapter", child.Title, child.Role)
		}
	}
}

func TestEPUBMajorUnitFallbackDoesNotPromoteTinyEntries(t *testing.T) {
	blocks := []domain.Block{
		{Seq: 0, Kind: domain.BlockParagraph, Text: "short"},
		{Seq: 1, Kind: domain.BlockParagraph, Text: "tiny"},
	}
	root := &domain.OutlineNode{Children: []*domain.OutlineNode{
		{Title: "One", Role: domain.RoleUnknown, StartBlock: 0, EndBlockExclusive: 1},
		{Title: "Two", Role: domain.RoleUnknown, StartBlock: 1, EndBlockExclusive: 2},
	}}
	classifyMajorUnits(root, blocks)
	for _, child := range root.Children {
		if child.Role != domain.RoleUnknown {
			t.Errorf("tiny entry %q was promoted to role %q", child.Title, child.Role)
		}
	}
}

func TestTOCSuppressionDoesNotHideOrdinaryChapterLinks(t *testing.T) {
	data := []byte(`<html><body><section epub:type="chapter"><h1>Chapter One</h1><p>Read the <a href="#details">important details</a> here.</p></section></body></html>`)
	parsed, err := parseContentFile(context.Background(), "chapter.xhtml", 0, 0, data)
	if err != nil {
		t.Fatal(err)
	}
	if suppressTOCPage(&parsed, "other-nav.xhtml") {
		t.Fatal("ordinary chapter with an internal link was classified as a TOC page")
	}
	for _, block := range parsed.blocks {
		if block.SkipByDefault {
			t.Errorf("ordinary chapter block was marked skipped: %q", block.Text)
		}
	}
}

func TestDirectParagraphFootnoteBecomesSkippedNoteBlock(t *testing.T) {
	data := []byte(`<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><p epub:type="footnote" id="note-direct">This direct paragraph footnote is retained.</p><p>Body text with a <a epub:type="noteref" href="#note-direct">note marker</a>.</p></body></html>`)
	parsed, err := parseContentFile(context.Background(), "notes.xhtml", 0, 0, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.blocks) != 2 {
		t.Fatalf("got %d blocks, want note plus body: %#v", len(parsed.blocks), parsed.blocks)
	}
	note := parsed.blocks[0]
	if note.Kind != domain.BlockNote || !note.SkipByDefault || !contains(note.Semantics, "footnote") || !strings.Contains(note.Text, "direct paragraph footnote") {
		t.Fatalf("direct paragraph footnote was not retained as a skipped note block: %#v", note)
	}
	if strings.Contains(parsed.blocks[1].Text, "note marker") || strings.Contains(parsed.blocks[1].Text, "direct paragraph footnote") {
		t.Fatalf("footnote body or reference interrupted ordinary prose: %q", parsed.blocks[1].Text)
	}
}

func TestParserHonorsCancellationAndRejectsNonZIPInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewParser().Parse(ctx, fixtureInput("nested")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context cancellation", err)
	}

	path := filepath.Join(t.TempDir(), "broken.epub")
	if err := os.WriteFile(path, []byte("not an EPUB"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewParser().Parse(context.Background(), source.Input{Name: "broken.epub", Path: path}); err == nil {
		t.Fatal("non-ZIP input was accepted as EPUB")
	}
}

func TestArchiveRejectsTraversalAndDuplicateEntries(t *testing.T) {
	for _, names := range [][]string{{"../escape"}, {"same", "same"}} {
		data := makeZip(t, names...)
		_, err := openEPUB(context.Background(), source.Input{Name: "bad.epub", R: bytes.NewReader(data)})
		if err == nil {
			t.Fatalf("archive names %v were accepted", names)
		}
	}
}

func TestResolveArchiveReferenceRejectsExternalAndEscapingPaths(t *testing.T) {
	for _, href := range []string{"https://example.com/book.xhtml", "../../../secret.xhtml", "%2e%2e/%2e%2e/secret.xhtml", "\\\\server\\book.xhtml"} {
		if _, _, err := resolveArchiveReference("OEBPS/nav.xhtml", href); err == nil {
			t.Errorf("accepted unsafe/external reference %q", href)
		}
	}
	path, fragment, err := resolveArchiveReference("OEBPS/nav.xhtml", "Text/chapter.xhtml#part-1")
	if err != nil || path != "OEBPS/Text/chapter.xhtml" || fragment != "part-1" {
		t.Fatalf("resolved reference = (%q, %q, %v)", path, fragment, err)
	}
}

func fixtureInput(name string) source.Input {
	if name == "broken_fragment" {
		return source.Input{Name: name + ".epub", R: bytes.NewReader(makeBrokenFragmentEPUB())}
	}
	if name == "nonlinear_navigation" {
		return source.Input{Name: name + ".epub", R: bytes.NewReader(makeNonlinearNavigationEPUB())}
	}
	if name == "broken_navigation" {
		return source.Input{Name: name + ".epub", R: bytes.NewReader(makeBrokenNavigationResourceEPUB())}
	}
	return source.Input{
		Name: name + ".epub",
		Path: filepath.Join("..", "..", "..", "testdata", "epub3_nested", name+".epub"),
	}
}

func makeBrokenNavigationResourceEPUB() []byte {
	files := []struct{ name, content string }{
		{"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"OEBPS/package.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Broken Navigation Book</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/><item id="unsupported" href="supplement.pdf" media-type="application/pdf"/></manifest><spine><itemref idref="chapter"/><itemref idref="unsupported"/></spine></package>`},
		{"OEBPS/nav.xhtml", `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><h1>Contents</h1><ol><li><a href="ghost.xhtml#missing">Lost Appendix</a></li><li><a href="supplement.pdf#page">Unsupported Appendix</a></li></ol></nav></body></html>`},
		{"OEBPS/chapter.xhtml", `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Chapter One</h1><p>This is the readable chapter.</p></body></html>`},
	}
	return zipFixture(files)
}

func makeNonlinearNavigationEPUB() []byte {
	files := []struct{ name, content string }{
		{"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"OEBPS/package.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Auxiliary Navigation Book</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/><item id="supplement" href="supplement.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/><itemref idref="supplement" linear="no"/></spine></package>`},
		{"OEBPS/nav.xhtml", `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><h1>Contents</h1><ol><li><a href="chapter.xhtml#chapter-one">Chapter One</a></li><li><span>Supplementary Material</span><ol><li><a href="supplement.xhtml#appendix">Supplement</a></li></ol></li></ol></nav></body></html>`},
		{"OEBPS/chapter.xhtml", `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml"><body><h1 id="chapter-one">Chapter One</h1><p>This is the linear chapter.</p></body></html>`},
		{"OEBPS/supplement.xhtml", `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml"><body><h1 id="appendix">Supplement</h1><p>Supplement body is excluded from default order.</p></body></html>`},
	}
	return zipFixture(files)
}

func makeBrokenFragmentEPUB() []byte {
	files := []struct{ name, content string }{
		{"META-INF/container.xml", `<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0"><rootfiles><rootfile full-path="OEBPS/package.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"OEBPS/package.opf", `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Broken Fragment Book</dc:title><dc:language>en</dc:language></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/></spine></package>`},
		{"OEBPS/nav.xhtml", `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body><nav epub:type="toc"><h1>Contents</h1><ol><li><a href="chapter.xhtml#missing-heading">Chapter One</a></li></ol></nav></body></html>`},
		{"OEBPS/chapter.xhtml", `<!doctype html><html xmlns="http://www.w3.org/1999/xhtml"><body><h1 id="chapter-one">Chapter One</h1><p>This chapter has readable text.</p></body></html>`},
	}
	return zipFixture(files)
}

func zipFixture(files []struct{ name, content string }) []byte {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range files {
		entry, err := writer.Create(file.name)
		if err != nil {
			panic(err)
		}
		_, _ = io.WriteString(entry, file.content)
	}
	if err := writer.Close(); err != nil {
		panic(err)
	}
	return output.Bytes()
}

func mustParseFixture(t *testing.T, name string) *domain.Document {
	t.Helper()
	document, err := NewParser().Parse(context.Background(), fixtureInput(name))
	if err != nil {
		t.Fatalf("parse %s fixture: %v", name, err)
	}
	return document
}

func findOutline(nodes []*domain.OutlineNode, title string) *domain.OutlineNode {
	for _, node := range nodes {
		if node.Title == title {
			return node
		}
		if found := findOutline(node.Children, title); found != nil {
			return found
		}
	}
	return nil
}

func warningContains(warnings []string, substring string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, substring) {
			return true
		}
	}
	return false
}

func makeZip(t *testing.T, names ...string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for index, name := range names {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create ZIP entry: %v", err)
		}
		_, _ = fmt.Fprintf(entry, "entry %d", index)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP: %v", err)
	}
	return output.Bytes()
}
