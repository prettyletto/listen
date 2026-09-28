package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/apperr"
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/settings"
	"github.com/prettyletto/listen/internal/source"
)

func TestRunPrintsHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}} {
		var stdout bytes.Buffer
		if err := Run(context.Background(), args, strings.NewReader(""), &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run(%v): %v", args, err)
		}
		if !strings.Contains(stdout.String(), "listen <source>") || !strings.Contains(stdout.String(), "--export [DIRECTORY]") || !strings.Contains(stdout.String(), "--translate BCP-47") {
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

func TestRunRejectsUnimplementedFormatsHonestly(t *testing.T) {
	err := Run(context.Background(), []string{"book.pdf"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if !apperr.HasCode(err, "format_not_ready") {
		t.Fatalf("got %v, want format_not_ready", err)
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
