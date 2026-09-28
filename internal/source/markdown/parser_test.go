package markdown

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/segment"
	"github.com/prettyletto/listen/internal/source"
)

var markdownFixtures = []string{"nested", "semantics"}

func TestMarkdownFixturesMatchGolden(t *testing.T) {
	for _, name := range markdownFixtures {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "testdata", "markdown", name+".md")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := NewParser().Parse(context.Background(), source.Input{Name: path, Path: path, R: bytes.NewReader(data)})
			if err != nil {
				t.Fatalf("parse fixture: %v", err)
			}
			actual, err := json.MarshalIndent(goldenDocumentFrom(doc), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			actual = append(actual, '\n')
			goldenPath := filepath.Join("..", "..", "..", "testdata", "expected", "markdown", name+".json")
			if os.Getenv("UPDATE_MARKDOWN_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %q: %v (set UPDATE_MARKDOWN_GOLDEN=1 after reviewing the parser change)", goldenPath, err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatalf("parsed document differs from %s; set UPDATE_MARKDOWN_GOLDEN=1 only after reviewing the parser change", goldenPath)
			}
		})
	}
}

func TestMarkdownPreservesSemanticsAndSkipsCodeByDefault(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "markdown", "semantics.md")
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: path, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Title != "Fixture Reader" || !reflect.DeepEqual(doc.Metadata.Authors, []string{"Ada Lovelace"}) || doc.Metadata.Language != "en" {
		t.Fatalf("front matter metadata was not extracted: %#v", doc.Metadata)
	}
	var listItems, quotes, rows, code, frontMatter int
	for _, block := range doc.Blocks {
		switch block.Kind {
		case domain.BlockListItem:
			listItems++
		case domain.BlockQuote:
			quotes++
		case domain.BlockTableRow:
			rows++
		case domain.BlockCode:
			if !block.SkipByDefault {
				t.Errorf("code block is not skipped by default: %#v", block)
			}
			if has(block.Semantics, "front_matter") {
				frontMatter++
			} else {
				code++
			}
		}
	}
	if listItems != 2 || quotes != 1 || rows != 3 || code != 2 || frontMatter != 1 {
		t.Fatalf("Markdown block semantics list=%d quote=%d rows=%d code=%d frontmatter=%d: %#v", listItems, quotes, rows, code, frontMatter, doc.Blocks)
	}
	plan, err := segment.PlanDocument(doc, segment.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.Segments {
		if strings.Contains(item.SpokenText, "func ignored") || strings.Contains(item.SpokenText, "var indented") {
			t.Errorf("code reached the default speech plan: %q", item.SpokenText)
		}
	}
	if !outlineRangesValid(doc.Outline, uint64(len(doc.Blocks))) {
		t.Fatalf("invalid heading intervals: %#v", doc.Outline)
	}
}

func TestMarkdownParserProbeAndCancellation(t *testing.T) {
	parser := NewParser()
	if score, err := parser.Probe(context.Background(), source.Input{Name: "chapter.md"}); err != nil || score != 100 {
		t.Fatalf("Markdown probe=(%d,%v), want (100,nil)", score, err)
	}
	if score, err := parser.Probe(context.Background(), source.Input{Name: "stdin", Path: "chapter.markdown"}); err != nil || score != 100 {
		t.Fatalf("Markdown path fallback probe=(%d,%v), want (100,nil)", score, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parser.Parse(ctx, source.Input{Name: "book.md", R: strings.NewReader("# Book")}); err != context.Canceled {
		t.Fatalf("canceled parse error = %v", err)
	}
}

func TestMarkdownSupportsTOMLAndYAMLAuthorLists(t *testing.T) {
	for _, input := range []string{
		"+++\ntitle = \"TOML Book\"\nauthors = [\"Ada Lovelace\", \"Grace Hopper\"]\nlanguage = \"en\"\n+++\n\n# TOML Book\n\nProse.\n",
		"---\ntitle: YAML Book\nauthors:\n  - Ada Lovelace\n  - Grace Hopper\nlanguage: en\n---\n\n# YAML Book\n\nProse.\n",
	} {
		doc, err := NewParser().Parse(context.Background(), source.Input{Name: "metadata.md", R: strings.NewReader(input)})
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Metadata.Authors) != 2 || doc.Metadata.Authors[0] != "Ada Lovelace" || doc.Metadata.Authors[1] != "Grace Hopper" {
			t.Errorf("author metadata = %#v", doc.Metadata.Authors)
		}
		if doc.Metadata.Language != "en" || doc.Metadata.Title == "" {
			t.Errorf("metadata = %#v", doc.Metadata)
		}
	}
}

type goldenDocument struct {
	Format   domain.Format   `json:"format"`
	Metadata domain.Metadata `json:"metadata"`
	Blocks   []goldenBlock   `json:"blocks"`
	Outline  []goldenNode    `json:"outline"`
	Warnings []string        `json:"warnings"`
}

type goldenBlock struct {
	Seq       uint64           `json:"seq"`
	Kind      domain.BlockKind `json:"kind"`
	Text      string           `json:"text"`
	Level     int              `json:"level,omitempty"`
	ByteStart int64            `json:"byte_start"`
	ByteEnd   int64            `json:"byte_end"`
	Semantics []string         `json:"semantics,omitempty"`
	Skip      bool             `json:"skip_by_default,omitempty"`
}

type goldenNode struct {
	Title             string                 `json:"title"`
	Role              domain.OutlineRole     `json:"role"`
	Depth             int                    `json:"depth"`
	StartBlock        uint64                 `json:"start_block"`
	EndBlockExclusive uint64                 `json:"end_block_exclusive"`
	Confidence        domain.Confidence      `json:"confidence"`
	Provenance        domain.StructureSource `json:"provenance"`
	Children          []goldenNode           `json:"children,omitempty"`
}

func goldenDocumentFrom(doc *domain.Document) goldenDocument {
	result := goldenDocument{Format: doc.Format, Metadata: doc.Metadata, Warnings: doc.Warnings}
	for _, block := range doc.Blocks {
		result.Blocks = append(result.Blocks, goldenBlock{Seq: block.Seq, Kind: block.Kind, Text: block.Text, Level: block.Level, ByteStart: block.Source.ByteStart, ByteEnd: block.Source.ByteEnd, Semantics: block.Semantics, Skip: block.SkipByDefault})
	}
	for _, node := range doc.Outline {
		result.Outline = append(result.Outline, goldenNodeFrom(node))
	}
	return result
}

func goldenNodeFrom(node *domain.OutlineNode) goldenNode {
	result := goldenNode{Title: node.Title, Role: node.Role, Depth: node.Depth, StartBlock: node.StartBlock, EndBlockExclusive: node.EndBlockExclusive, Confidence: node.Confidence, Provenance: node.Provenance}
	for _, child := range node.Children {
		result.Children = append(result.Children, goldenNodeFrom(child))
	}
	return result
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

func has(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

var _ source.Parser = Parser{}
