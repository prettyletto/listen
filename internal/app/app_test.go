package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/source"
	gocodesource "github.com/prettyletto/listen/internal/source/gocode"
	"github.com/prettyletto/listen/internal/speech"
	"github.com/prettyletto/listen/internal/speech/kokoro"
)

func TestRunPrintsHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}} {
		var stdout bytes.Buffer
		if err := Run(context.Background(), args, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run(%v): %v", args, err)
		}
		if !strings.Contains(stdout.String(), "listen <source>") || !strings.Contains(stdout.String(), "--export [DIRECTORY]") || !strings.Contains(stdout.String(), "--translate BCP-47") || !strings.Contains(stdout.String(), "--code") || !strings.Contains(stdout.String(), "documentation directories") {
			t.Fatalf("Run(%v) did not explain the default command: %q", args, stdout.String())
		}
	}
}

func TestModelsCommandListsConfiguredProviderModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	var stdout bytes.Buffer
	err := runWithRuntime(context.Background(), []string{"models"}, strings.NewReader(""), &stdout, &bytes.Buffer{}, runtimeDeps{
		discoverCloudModels: func(context.Context, settings.Config) ([]string, error) {
			return []string{"gpt-test-small", "gpt-test-large"}, nil
		},
	})
	if err != nil {
		t.Fatalf("models command: %v", err)
	}
	if !strings.Contains(stdout.String(), "gpt-test-small") || !strings.Contains(stdout.String(), "gpt-test-large") {
		t.Fatalf("model list output = %q", stdout.String())
	}
}

func TestTranslateFlagReportsMissingLocalBackendWithoutReadingSource(t *testing.T) {
	err := Run(context.Background(), []string{"missing.txt", "--translate", "pt-BR"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !apperr.HasCode(err, "translation_not_ready") || err.Error() != "Local translation is not ready." {
		t.Fatalf("got %v, want actionable translation_not_ready", err)
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, []string{"book.epub"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !apperr.HasCode(err, "interrupted") {
		t.Fatalf("got %v, want interrupted error", err)
	}
}

func TestParseListenOptionsAcceptsFlagsBeforeAndAfterSource(t *testing.T) {
	for _, args := range [][]string{
		{"--voice=pf_dora", "--speed", "1.2", "book.txt", "--no-play"},
		{"book.txt", "--voice", "pf_dora", "--speed=1.2", "--no-play"},
		{"--translate", "pt-br", "book.txt", "--voice=pf_dora"},
	} {
		options, err := parseListenOptions(args)
		if err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		if options.source != "book.txt" || options.voice != "pf_dora" {
			t.Fatalf("unexpected parsed options for %v: %#v", args, options)
		}
		if args[0] != "--translate" && (options.speed != 1.2 || !options.noPlay) {
			t.Fatalf("unexpected parsed options for %v: %#v", args, options)
		}
		if args[0] == "--translate" && options.targetLanguage != "pt-BR" {
			t.Fatalf("target language was not normalized: %#v", options)
		}
	}
	if _, err := parseListenOptions([]string{"book.txt", "--translate", "pt--BR"}); err == nil {
		t.Fatal("accepted a malformed BCP-47 target language")
	}
}

func TestParseListenOptionsSupportsCodeNarrationFlag(t *testing.T) {
	for _, args := range [][]string{{"--code", "main.go"}, {"main.go", "--code"}} {
		options, err := parseListenOptions(args)
		if err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		if options.source != "main.go" || !options.code {
			t.Fatalf("code narration options = %#v", options)
		}
	}
	if _, err := parseListenOptions([]string{"main.go", "--code=true"}); err == nil {
		t.Fatal("accepted a value for the code narration flag")
	}
}

func TestCodeNarrationIsOptInAndRunsThroughTheSpeechPipeline(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	var spoken []string
	deps := runtimeDeps{
		startSynth: func(_ context.Context, _ kokoro.Options) (speech.Synthesizer, error) {
			return appTestSynth{t: t, onRequest: func(request speech.Request) {
				spoken = append(spoken, request.Segment.SpokenText)
			}}, nil
		},
	}
	path := filepath.Join("..", "..", "testdata", "go", "narration.go")
	if err := runWithRuntime(context.Background(), []string{path, "--code", "--no-play"}, nil, io.Discard, io.Discard, deps); err != nil {
		t.Fatalf("listen to Go source: %v", err)
	}
	joined := strings.Join(spoken, "\n")
	for _, want := range []string{"Package catalog", "Function load accepts", "If the error reported by ctx"} {
		if !strings.Contains(joined, want) {
			t.Errorf("spoken code narration is missing %q:\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"func Load(", "ctx.Err() !=", "io.ReadAll("} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("raw Go punctuation reached speech (%q):\n%s", unwanted, joined)
		}
	}
}

func TestCodeNarrationParserIsOnlySelectedWithCodeFlag(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "go", "narration.go")
	ordinary, err := parseDocument(context.Background(), source.Input{Name: "narration.go", Path: path})
	if err != nil {
		t.Fatalf("parse ordinary Go source: %v", err)
	}
	if ordinary.Format != domain.FormatText {
		t.Fatalf("default Go parsing format = %q, want plain text", ordinary.Format)
	}
	narrated, err := parseDocumentWithCodeMode(context.Background(), source.Input{Name: "narration.go", Path: path}, true)
	if err != nil {
		t.Fatalf("parse Go source in code mode: %v", err)
	}
	if narrated.Format != domain.FormatCode {
		t.Fatalf("--code parsing format = %q, want code", narrated.Format)
	}
}

func TestCodeFlagRejectsUnsupportedFormatsWithActionableError(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "markdown", "nested.md")
	err := Run(context.Background(), []string{path, "--code"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !apperr.HasCode(err, "code_source_unsupported") {
		t.Fatalf("got %v, want code_source_unsupported", err)
	}
	var appError *apperr.Error
	if !errors.As(err, &appError) || !strings.Contains(appError.Hint, ".go") {
		t.Fatalf("unsupported code source error did not name the required extension: %#v", appError)
	}
}

func TestCodeNarrationReportsResourceLimitsActionably(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path := filepath.Join(home, "large.go")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(gocodesource.MaxSourceBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	err = runWithRuntime(context.Background(), []string{path, "--code"}, strings.NewReader(""), io.Discard, io.Discard, runtimeDeps{})
	if !apperr.HasCode(err, "code_resource_limit") {
		t.Fatalf("got %v, want code_resource_limit", err)
	}
	var appError *apperr.Error
	if !errors.As(err, &appError) || !strings.Contains(appError.Hint, "smaller Go files") {
		t.Fatalf("resource limit error lacks actionable hint: %#v", appError)
	}
}

func TestParseListenOptionsAcceptsExportWithOptionalDirectory(t *testing.T) {
	for _, test := range []struct {
		args []string
		path string
	}{
		{args: []string{"book.epub", "--export"}},
		{args: []string{"book.epub", "--export", "book-output"}, path: "book-output"},
		{args: []string{"--export=book-output", "book.epub"}, path: "book-output"},
	} {
		options, err := parseListenOptions(test.args)
		if err != nil {
			t.Fatalf("parse %v: %v", test.args, err)
		}
		if !options.exportAudio || options.exportPath != test.path {
			t.Fatalf("unexpected export options for %v: %#v", test.args, options)
		}
	}
	if _, err := parseListenOptions([]string{"book.epub", "--export="}); err == nil {
		t.Fatal("accepted an empty export directory")
	}
}

func TestParseDocumentSelectsEPUBParser(t *testing.T) {
	path := "../../testdata/epub3_nested/nested.epub"
	doc, err := parseDocument(context.Background(), source.Input{Name: "nested.epub", Path: path})
	if err != nil {
		t.Fatalf("parse EPUB: %v", err)
	}
	if doc.Format != domain.FormatEPUB || doc.Metadata.Title != "Nested Book" {
		t.Fatalf("unexpected parsed EPUB: format=%q title=%q", doc.Format, doc.Metadata.Title)
	}
}

func TestParseDocumentSelectsMarkdownParser(t *testing.T) {
	path := "../../testdata/markdown/nested.md"
	doc, err := parseDocument(context.Background(), source.Input{Name: "nested.md", Path: path})
	if err != nil {
		t.Fatalf("parse Markdown: %v", err)
	}
	if doc.Format != domain.FormatMarkdown || doc.Metadata.Title != "Example Book" {
		t.Fatalf("unexpected parsed Markdown: format=%q title=%q", doc.Format, doc.Metadata.Title)
	}
}

func TestParseDocumentSelectsHTMLParser(t *testing.T) {
	path := "../../testdata/html/main-content.html"
	doc, err := parseDocument(context.Background(), source.Input{Name: "main-content.html", Path: path})
	if err != nil {
		t.Fatalf("parse HTML: %v", err)
	}
	if doc.Format != domain.FormatHTML || doc.Metadata.Title != "After the Storm | City Daily" {
		t.Fatalf("unexpected parsed HTML: format=%q title=%q", doc.Format, doc.Metadata.Title)
	}
}

func TestParseDocumentSelectsPDFParser(t *testing.T) {
	for _, name := range []string{"pdftohtml", "pdfimages"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("Poppler PDF tools are not installed")
		}
	}
	path := "../../testdata/pdf/born-digital.pdf"
	doc, err := parseDocument(context.Background(), source.Input{Name: "born-digital.pdf", Path: path})
	if err != nil {
		t.Fatalf("parse PDF: %v", err)
	}
	wantTitle := "born-digital"
	if _, err := exec.LookPath("pdfinfo"); err == nil {
		wantTitle = "The Sample Book"
	}
	if doc.Format != domain.FormatPDF || doc.Metadata.Title != wantTitle {
		t.Fatalf("unexpected parsed PDF: format=%q title=%q", doc.Format, doc.Metadata.Title)
	}
}

func TestListenDocumentationDirectoryUsesOrderedCompositeDocument(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	var spoken []string
	var sourceOrder []string
	path, err := filepath.Abs(filepath.Join("..", "..", "testdata", "directories", "repository", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	deps := runtimeDeps{
		startSynth: func(_ context.Context, _ kokoro.Options) (speech.Synthesizer, error) {
			return appTestSynth{t: t, onRequest: func(request speech.Request) {
				spoken = append(spoken, request.Segment.SpokenText)
				relative, err := filepath.Rel(path, request.Segment.Source.Path)
				if err != nil {
					t.Errorf("relative source path: %v", err)
				}
				sourceOrder = append(sourceOrder, filepath.ToSlash(relative))
			}}, nil
		},
	}
	if err := runWithRuntime(context.Background(), []string{path, "--no-play"}, nil, io.Discard, io.Discard, deps); err != nil {
		t.Fatalf("listen to documentation directory: %v", err)
	}
	joined := strings.Join(spoken, "\n")
	ordered := []string{
		"Start with the architecture guide.",
		"Documents preserve reading order and source structure.",
		"Go owns parsing and deterministic ordering.",
	}
	previous := -1
	for _, text := range ordered {
		position := strings.Index(joined, text)
		if position <= previous {
			t.Fatalf("documentation session did not preserve priority order for %q: %q", text, joined)
		}
		previous = position
	}
	wantSourceOrder := []string{
		"README.md", "adr/0002-document-ir.md", "architecture.md",
	}
	if !reflect.DeepEqual(sourceOrder, wantSourceOrder) {
		t.Fatalf("synthesized file order = %#v, want %#v", sourceOrder, wantSourceOrder)
	}
}

func TestListenHTMLFixtureSynthesizesSelectedContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	var spoken []string
	deps := runtimeDeps{
		startSynth: func(_ context.Context, _ kokoro.Options) (speech.Synthesizer, error) {
			return appTestSynth{t: t, onRequest: func(request speech.Request) {
				spoken = append(spoken, request.Segment.SpokenText)
			}}, nil
		},
	}
	path := "../../testdata/html/main-content.html"
	if err := runWithRuntime(context.Background(), []string{path, "--no-play"}, nil, io.Discard, io.Discard, deps); err != nil {
		t.Fatalf("listen HTML fixture: %v", err)
	}
	text := strings.Join(spoken, " ")
	if !strings.Contains(text, "The first rescue boats reached the riverside") || !strings.Contains(text, "What happened") {
		t.Fatalf("main content was not synthesized: %q", text)
	}
	for _, unwanted := range []string{"Recommended story", "Copyright City Daily", "Subscribe for updates"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("site chrome reached synthesized speech: %q", unwanted)
		}
	}
}

func TestListenPDFFixtureSynthesizesReconstructedContent(t *testing.T) {
	for _, name := range []string{"pdftohtml", "pdfimages"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("Poppler PDF tools are not installed")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	var spoken []string
	deps := runtimeDeps{
		startSynth: func(_ context.Context, _ kokoro.Options) (speech.Synthesizer, error) {
			return appTestSynth{t: t, onRequest: func(request speech.Request) {
				spoken = append(spoken, request.Segment.SpokenText)
			}}, nil
		},
	}
	path := "../../testdata/pdf/born-digital.pdf"
	if err := runWithRuntime(context.Background(), []string{path, "--no-play"}, nil, io.Discard, io.Discard, deps); err != nil {
		t.Fatalf("listen PDF fixture: %v", err)
	}
	text := strings.Join(spoken, " ")
	for _, wanted := range []string{"Chapter 1: The Beginning", "Left column begins with an opening paragraph", "Right column follows the complete left column"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("PDF content did not reach synthesized speech: %q missing from %q", wanted, text)
		}
	}
	for _, unwanted := range []string{"The Sample Book", "(1)", "(2)", "(3)"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("repeated page furniture reached synthesized speech: %q", unwanted)
		}
	}
}

func TestRunReportsScannedPDFAsNeedingOCR(t *testing.T) {
	for _, name := range []string{"pdftohtml", "pdfimages"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("Poppler PDF tools are not installed")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	err := Run(context.Background(), []string{"../../testdata/pdf/scanned.pdf", "--no-play"}, strings.NewReader(""), io.Discard, io.Discard)
	if !apperr.HasCode(err, "pdf_needs_ocr") {
		t.Fatalf("got %v, want actionable pdf_needs_ocr error", err)
	}
	var appError *apperr.Error
	if !errors.As(err, &appError) || !strings.Contains(appError.Hint, "OCR is not available") {
		t.Fatalf("scanned-PDF error did not explain the current limitation: %#v", appError)
	}
}
