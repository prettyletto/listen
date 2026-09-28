package html

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
	textsource "github.com/prettyletto/listen/internal/source/text"
)

var htmlFixtures = []string{"main-content", "dominant-article", "body-fallback"}

func TestHTMLFixturesMatchGolden(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range htmlFixtures {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, "testdata", "html", name+".html")
			doc, err := NewParser().Parse(context.Background(), source.Input{Name: path, Path: path})
			if err != nil {
				t.Fatalf("parse HTML fixture: %v", err)
			}
			actual, err := json.MarshalIndent(goldenDocumentFrom(doc), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			actual = append(actual, '\n')
			goldenPath := filepath.Join(root, "testdata", "expected", "html", name+".json")
			if os.Getenv("UPDATE_HTML_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %q: %v (set UPDATE_HTML_GOLDEN=1 after reviewing the parser output)", goldenPath, err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatalf("parsed document differs from %s; set UPDATE_HTML_GOLDEN=1 only after reviewing the parser change", goldenPath)
			}
		})
	}
}

func TestHTMLUsesMainBeforeArticlesAndSuppressesSiteChrome(t *testing.T) {
	doc := parseFixture(t, "main-content")
	if doc.Format != domain.FormatHTML || doc.Metadata.Title != "After the Storm | City Daily" || doc.Metadata.Language != "en" {
		t.Fatalf("unexpected document metadata: %#v", doc)
	}
	allText := blocksText(doc.Blocks)
	for _, unwanted := range []string{"Recommended story", "This article is outside main", "Subscribe for updates", "Copyright City Daily", "Sidebar text", "News", "headOnlyText", "bodyOnlyText"} {
		if strings.Contains(allText, unwanted) {
			t.Errorf("site chrome or out-of-main article reached the reading blocks: %q", unwanted)
		}
	}
	for _, wanted := range []string{"After the Storm", "By Ada Lovelace", "warm blankets", "We could see the lights coming through the rain.", "What happened", "Fresh water arrived at noon.", "Response times", "North crew; 6:10"} {
		if !strings.Contains(allText, wanted) {
			t.Errorf("main content is missing %q", wanted)
		}
	}
	if !hasBlock(doc.Blocks, domain.BlockHeading, "What happened", 2) {
		t.Error("DOM heading level was not preserved")
	}
	if !hasKind(doc.Blocks, domain.BlockQuote) || !hasKind(doc.Blocks, domain.BlockListItem) || !hasKind(doc.Blocks, domain.BlockTableRow) {
		t.Errorf("quote/list/table semantics were not preserved: %#v", doc.Blocks)
	}
	if !hasSemantic(doc.Blocks, "table_header") {
		t.Errorf("table heading row lost its header semantics: %#v", doc.Blocks)
	}
	if len(doc.Warnings) != 0 {
		t.Fatalf("main selection produced unexpected warnings: %#v", doc.Warnings)
	}
	if !validOutline(doc.Outline, uint64(len(doc.Blocks))) {
		t.Fatalf("invalid HTML outline ranges: %#v", doc.Outline)
	}
}

func TestHTMLUsesTheDominantArticleWhenMainIsAbsent(t *testing.T) {
	doc := parseFixture(t, "dominant-article")
	if doc.Metadata.Title != "Uma ponte para casa" || doc.Metadata.Language != "pt-BR" {
		t.Fatalf("metadata = %#v", doc.Metadata)
	}
	allText := blocksText(doc.Blocks)
	for _, unwanted := range []string{"Site header", "Choose a section", "Weather", "brief forecast", "terms and privacy"} {
		if strings.Contains(allText, unwanted) {
			t.Errorf("non-dominant article or site chrome was extracted: %q", unwanted)
		}
	}
	if !strings.Contains(allText, "moradores atravessaram a ponte nova") {
		t.Fatalf("dominant article prose is missing: %q", allText)
	}
	if len(doc.Warnings) != 0 {
		t.Fatalf("dominant article selection produced warnings: %#v", doc.Warnings)
	}
}

func TestHTMLBodyFallbackSuppressesChromeAndUsesHeadingTitle(t *testing.T) {
	doc := parseFixture(t, "body-fallback")
	if doc.Metadata.Title != "Notes from the harbor" {
		t.Fatalf("metadata title = %q, want first content heading", doc.Metadata.Title)
	}
	if !reflect.DeepEqual(doc.Metadata.Authors, []string{"Grace Hopper"}) {
		t.Fatalf("author metadata = %#v", doc.Metadata.Authors)
	}
	allText := blocksText(doc.Blocks)
	for _, unwanted := range []string{"Magazine home", "Choose a story", "Issue navigation", "Subscribe for more stories", "Privacy, terms"} {
		if strings.Contains(allText, unwanted) {
			t.Errorf("body fallback included site chrome: %q", unwanted)
		}
	}
	if !strings.Contains(allText, "white lighthouse above the shore") {
		t.Errorf("meaningful image alt text was not preserved: %q", allText)
	}
	if len(doc.Warnings) != 1 || !strings.Contains(doc.Warnings[0], "body content was used") {
		t.Fatalf("body fallback warning = %#v", doc.Warnings)
	}
}

func TestHTMLParserProbeCancellationAndInvalidInput(t *testing.T) {
	parser := NewParser()
	for _, input := range []source.Input{{Name: "story.HTML"}, {Name: "unknown", Path: "story.htm"}} {
		if score, err := parser.Probe(context.Background(), input); err != nil || score != 100 {
			t.Errorf("probe(%#v) = (%d,%v), want (100,nil)", input, score, err)
		}
	}
	if score, err := parser.Probe(context.Background(), source.Input{Name: "notes.txt"}); err != nil || score != 0 {
		t.Errorf("text probe = (%d,%v), want (0,nil)", score, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parser.Parse(ctx, source.Input{Name: "story.html", R: strings.NewReader("<main>hello</main>")}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled parse error = %v, want context.Canceled", err)
	}
	if _, err := parser.Parse(context.Background(), source.Input{Name: "bad.html", R: strings.NewReader("\xff")}); !errors.Is(err, textsource.ErrInvalidUTF8) {
		t.Errorf("invalid UTF-8 error = %v", err)
	}
	if _, err := parser.Parse(context.Background(), source.Input{Name: "empty.html", R: strings.NewReader("<html><body><nav>Home</nav></body></html>")}); !errors.Is(err, textsource.ErrEmptySource) {
		t.Errorf("empty content error = %v", err)
	}
}

func TestHTMLBlocksCarryStableDOMProvenance(t *testing.T) {
	first := parseFixture(t, "main-content")
	second := parseFixture(t, "main-content")
	if first.ID != second.ID || len(first.Blocks) != len(second.Blocks) {
		t.Fatal("parsing the same HTML twice produced different document identity or block count")
	}
	for seq, block := range first.Blocks {
		if block.Seq != uint64(seq) {
			t.Errorf("block sequence at %d = %d", seq, block.Seq)
		}
		if block.Source.DOMPath == "" || block.Source.Path == "" || block.Source.SpineIndex != -1 {
			t.Errorf("block lost source provenance: %#v", block)
		}
		if block.ID != second.Blocks[seq].ID {
			t.Errorf("block %d ID changed between parses", seq)
		}
	}
}

func parseFixture(t *testing.T, name string) *domain.Document {
	t.Helper()
	path := filepath.Join("..", "..", "..", "testdata", "html", name+".html")
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: path, Path: path})
	if err != nil {
		t.Fatalf("parse %s fixture: %v", name, err)
	}
	return doc
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
	DOMPath   string           `json:"dom_path"`
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
		result.Blocks = append(result.Blocks, goldenBlock{Seq: block.Seq, Kind: block.Kind, Text: block.Text, Level: block.Level, DOMPath: block.Source.DOMPath, Semantics: block.Semantics, Skip: block.SkipByDefault})
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

func blocksText(blocks []domain.Block) string {
	var texts []string
	for _, block := range blocks {
		texts = append(texts, block.Text)
	}
	return strings.Join(texts, " ")
}

func hasBlock(blocks []domain.Block, kind domain.BlockKind, text string, level int) bool {
	for _, block := range blocks {
		if block.Kind == kind && block.Text == text && block.Level == level {
			return true
		}
	}
	return false
}

func hasKind(blocks []domain.Block, kind domain.BlockKind) bool {
	for _, block := range blocks {
		if block.Kind == kind {
			return true
		}
	}
	return false
}

func hasSemantic(blocks []domain.Block, semantic string) bool {
	for _, block := range blocks {
		for _, value := range block.Semantics {
			if value == semantic {
				return true
			}
		}
	}
	return false
}

func validOutline(nodes []*domain.OutlineNode, count uint64) bool {
	for _, node := range nodes {
		if node.StartBlock >= node.EndBlockExclusive || node.EndBlockExclusive > count || !validOutline(node.Children, count) {
			return false
		}
		for _, child := range node.Children {
			if child.StartBlock < node.StartBlock || child.EndBlockExclusive > node.EndBlockExclusive {
				return false
			}
		}
	}
	return true
}

var _ source.Parser = Parser{}
