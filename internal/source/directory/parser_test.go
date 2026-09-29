package directory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
	htmlsource "github.com/prettyletto/listen/internal/source/html"
	markdownsource "github.com/prettyletto/listen/internal/source/markdown"
	pdfsource "github.com/prettyletto/listen/internal/source/pdf"
	textsource "github.com/prettyletto/listen/internal/source/text"
)

func TestRepositoryDirectoryMatchesGoldenAndHasStableIdentity(t *testing.T) {
	root := fixturePath(t, "testdata", "directories", "repository")
	parser := NewParser(markdownsource.NewParser(), htmlsource.NewParser(), pdfsource.NewParser(), textsource.NewParser())
	input := source.Input{Name: filepath.Base(root), Path: root}
	first, err := parser.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("parse documentation directory: %v", err)
	}
	second, err := parser.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("parse documentation directory again: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated directory parses differ:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.Format != domain.FormatDirectory || first.Metadata.Title != "repository" {
		t.Fatalf("unexpected directory document metadata: %#v", first)
	}
	if len(first.Outline) != 1 || first.Outline[0].Role != domain.RoleBook || first.Outline[0].Provenance != domain.StructureDirectory {
		t.Fatalf("directory root outline is missing: %#v", first.Outline)
	}
	if !outlineRangesValid(first.Outline, uint64(len(first.Blocks))) {
		t.Fatalf("composite outline ranges are invalid: %#v", first.Outline)
	}
	for _, block := range first.Blocks {
		if !filepath.IsAbs(block.Source.Path) || !strings.HasPrefix(block.Source.Path, root+string(filepath.Separator)) {
			t.Fatalf("block lost source traceability: %#v", block)
		}
	}
	for _, unwanted := range []string{"This file stays outside the session.", "This draft is excluded", "This source file is outside"} {
		for _, block := range first.Blocks {
			if strings.Contains(block.Text, unwanted) {
				t.Fatalf("ignored or unsupported source entered the session: %q", block.Text)
			}
		}
	}

	actual, err := json.MarshalIndent(goldenFrom(root, first), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	goldenPath := fixturePath(t, "testdata", "expected", "directory", "repository.json")
	if os.Getenv("UPDATE_DIRECTORY_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read directory golden %q: %v (set UPDATE_DIRECTORY_GOLDEN=1 after reviewing parser output)", goldenPath, err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("parsed directory differs from %s; set UPDATE_DIRECTORY_GOLDEN=1 only after reviewing parser output", goldenPath)
	}
}

func TestDirectoryResolverAppliesRepositoryAndNestedIgnoreRules(t *testing.T) {
	repository := t.TempDir()
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(repository, ".gitignore"), "/docs/generated.md\n")
	root := filepath.Join(repository, "docs")
	writeFixture(t, filepath.Join(root, "README.md"), "# Guide\n\nRead the files.\n")
	writeFixture(t, filepath.Join(root, ".gitignore"), "*.draft.md\n!keep.draft.md\n")
	writeFixture(t, filepath.Join(root, "generated.md"), "# Generated\n\nDo not include.\n")
	writeFixture(t, filepath.Join(root, "drop.draft.md"), "# Draft\n\nDo not include.\n")
	writeFixture(t, filepath.Join(root, "keep.draft.md"), "# Kept\n\nInclude this file.\n")
	writeFixture(t, filepath.Join(root, "private", ".git", "private.md"), "# Private\n\nNever include this.\n")
	writeFixture(t, filepath.Join(root, "source.go"), "package docs\n")

	parser := NewParser(markdownsource.NewParser())
	doc, err := parser.Parse(context.Background(), source.Input{Name: "docs", Path: root})
	if err != nil {
		t.Fatalf("parse nested documentation directory: %v", err)
	}
	var texts []string
	for _, block := range doc.Blocks {
		texts = append(texts, block.Text)
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "Include this file") || strings.Contains(joined, "Do not include") || strings.Contains(joined, "Never include") {
		t.Fatalf("ignore rules produced unexpected contents: %q", joined)
	}
}

func TestDirectoryResolverRejectsEmptyAndHonorsCancellation(t *testing.T) {
	parser := NewParser(markdownsource.NewParser())
	root := t.TempDir()
	if _, err := parser.Parse(context.Background(), source.Input{Name: "empty", Path: root}); !errors.Is(err, ErrNoDocuments) {
		t.Fatalf("empty directory error = %v, want ErrNoDocuments", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parser.Parse(ctx, source.Input{Name: "empty", Path: root}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled directory parse error = %v", err)
	}
}

func TestDirectoryCompositeNamespacesRepeatedDocumentIDs(t *testing.T) {
	root := t.TempDir()
	content := "# Shared title\n\nThe same file can appear more than once.\n"
	writeFixture(t, filepath.Join(root, "a.md"), content)
	writeFixture(t, filepath.Join(root, "b.md"), content)
	parser := NewParser(markdownsource.NewParser())
	doc, err := parser.Parse(context.Background(), source.Input{Name: "copy", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	seenBlocks := map[string]bool{}
	for _, block := range doc.Blocks {
		if seenBlocks[block.ID] {
			t.Fatalf("duplicate block ID %q", block.ID)
		}
		seenBlocks[block.ID] = true
	}
	seenOutlines := map[string]bool{}
	var visit func([]*domain.OutlineNode)
	visit = func(nodes []*domain.OutlineNode) {
		for _, node := range nodes {
			if seenOutlines[node.ID] {
				t.Errorf("duplicate outline ID %q", node.ID)
			}
			seenOutlines[node.ID] = true
			visit(node.Children)
		}
	}
	visit(doc.Outline)
	if len(seenBlocks) != len(doc.Blocks) || len(seenOutlines) < 3 {
		t.Fatalf("composite IDs were not unique: blocks=%d outlines=%d", len(seenBlocks), len(seenOutlines))
	}
}

func TestDirectoryOrderIsStableAcrossFileCreationOrderAndRoots(t *testing.T) {
	files := []struct {
		path    string
		content string
	}{
		{path: "zeta.md", content: "# Zeta\n\nLast alphabetically.\n"},
		{path: "README.md", content: "# Overview\n\nFirst by priority.\n"},
		{path: "docs/adr/0001-choice.md", content: "# Decision\n\nSecond priority group.\n"},
		{path: "alpha.md", content: "# Alpha\n\nFirst alphabetically.\n"},
	}
	firstRoot, secondRoot := filepath.Join(t.TempDir(), "same-docs"), filepath.Join(t.TempDir(), "same-docs")
	for _, file := range files {
		writeFixture(t, filepath.Join(firstRoot, filepath.FromSlash(file.path)), file.content)
	}
	for index := len(files) - 1; index >= 0; index-- {
		file := files[index]
		writeFixture(t, filepath.Join(secondRoot, filepath.FromSlash(file.path)), file.content)
	}
	parser := NewParser(markdownsource.NewParser())
	first, err := parser.Parse(context.Background(), source.Input{Name: "first", Path: firstRoot})
	if err != nil {
		t.Fatal(err)
	}
	second, err := parser.Parse(context.Background(), source.Input{Name: "second", Path: secondRoot})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("directory identity depends on absolute root: %q != %q", first.ID, second.ID)
	}
	firstGolden, err := json.Marshal(goldenFrom(firstRoot, first))
	if err != nil {
		t.Fatal(err)
	}
	secondGolden, err := json.Marshal(goldenFrom(secondRoot, second))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstGolden, secondGolden) {
		t.Fatalf("directory content or order depends on file creation order:\nfirst=%s\nsecond=%s", firstGolden, secondGolden)
	}
}

func TestGitIgnoreGlobMatching(t *testing.T) {
	base := t.TempDir()
	for _, test := range []struct {
		pattern string
		value   string
		want    bool
	}{
		{pattern: "*.log", value: "nested/build.log", want: true},
		{pattern: "/root.md", value: "root.md", want: true},
		{pattern: "/root.md", value: "nested/root.md", want: false},
		{pattern: "logs/**/trace?.txt", value: "logs/a/b/trace1.txt", want: true},
		{pattern: "docs/[a-c].md", value: "docs/b.md", want: true},
		{pattern: "docs/[!a-c].md", value: "docs/z.md", want: true},
	} {
		rule := ignoreRule{base: base, pattern: test.pattern, anchored: strings.HasPrefix(test.pattern, "/")}
		rule.pattern = strings.TrimPrefix(rule.pattern, "/")
		if got := rule.matches(filepath.Join(base, filepath.FromSlash(test.value)), false); got != test.want {
			t.Errorf("ignore rule %q matches %q = %v, want %v", test.pattern, test.value, got, test.want)
		}
	}
}

func fixturePath(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type goldenDocument struct {
	Format   domain.Format   `json:"format"`
	Metadata domain.Metadata `json:"metadata"`
	Blocks   []goldenBlock   `json:"blocks"`
	Outline  []goldenNode    `json:"outline"`
	Warnings []string        `json:"warnings,omitempty"`
}

type goldenBlock struct {
	Seq       uint64           `json:"seq"`
	Kind      domain.BlockKind `json:"kind"`
	Text      string           `json:"text"`
	Level     int              `json:"level,omitempty"`
	Path      string           `json:"path"`
	ByteStart int64            `json:"byte_start,omitempty"`
	ByteEnd   int64            `json:"byte_end,omitempty"`
	Page      int              `json:"page,omitempty"`
	DOMPath   string           `json:"dom_path,omitempty"`
	Semantics []string         `json:"semantics,omitempty"`
	Skip      bool             `json:"skip_by_default,omitempty"`
}

type goldenNode struct {
	Title             string                 `json:"title"`
	Role              domain.OutlineRole     `json:"role"`
	Depth             int                    `json:"depth"`
	StartBlock        uint64                 `json:"start_block"`
	EndBlockExclusive uint64                 `json:"end_block_exclusive"`
	TargetPath        string                 `json:"target_path,omitempty"`
	Confidence        domain.Confidence      `json:"confidence"`
	Provenance        domain.StructureSource `json:"provenance"`
	Children          []goldenNode           `json:"children,omitempty"`
}

func goldenFrom(root string, doc *domain.Document) goldenDocument {
	result := goldenDocument{Format: doc.Format, Metadata: doc.Metadata, Warnings: doc.Warnings}
	for _, block := range doc.Blocks {
		result.Blocks = append(result.Blocks, goldenBlock{
			Seq: block.Seq, Kind: block.Kind, Text: block.Text, Level: block.Level,
			Path: relativeSourcePath(root, block.Source.Path), ByteStart: block.Source.ByteStart,
			ByteEnd: block.Source.ByteEnd, Page: block.Source.Page, DOMPath: block.Source.DOMPath,
			Semantics: block.Semantics, Skip: block.SkipByDefault,
		})
	}
	for _, node := range doc.Outline {
		result.Outline = append(result.Outline, goldenOutline(root, node))
	}
	return result
}

func goldenOutline(root string, node *domain.OutlineNode) goldenNode {
	result := goldenNode{
		Title: node.Title, Role: node.Role, Depth: node.Depth, StartBlock: node.StartBlock,
		EndBlockExclusive: node.EndBlockExclusive, TargetPath: relativeSourcePath(root, node.SourceTarget.Path),
		Confidence: node.Confidence, Provenance: node.Provenance,
	}
	for _, child := range node.Children {
		result.Children = append(result.Children, goldenOutline(root, child))
	}
	return result
}

func relativeSourcePath(root, path string) string {
	if path == "" {
		return ""
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(relative)
}

func outlineRangesValid(nodes []*domain.OutlineNode, count uint64) bool {
	for _, node := range nodes {
		if node.StartBlock > node.EndBlockExclusive || node.EndBlockExclusive > count {
			return false
		}
		for _, child := range node.Children {
			if child.StartBlock < node.StartBlock || child.EndBlockExclusive > node.EndBlockExclusive || !outlineRangesValid([]*domain.OutlineNode{child}, count) {
				return false
			}
		}
	}
	return true
}
