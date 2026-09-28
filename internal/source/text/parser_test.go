package text

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
	"github.com/prettyletto/listen/internal/source"
)

type goldenBlock struct {
	Seq       uint64           `json:"seq"`
	Kind      domain.BlockKind `json:"kind"`
	Text      string           `json:"text"`
	ByteStart int64            `json:"byte_start"`
	ByteEnd   int64            `json:"byte_end"`
}

func TestParserMatchesPlainTextGolden(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	inputPath := filepath.Join(root, "testdata", "plain", "paragraphs.txt")
	data, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var want []goldenBlock
	golden, err := os.ReadFile(filepath.Join(root, "testdata", "expected", "plain_document.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: inputPath, Path: inputPath, R: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]goldenBlock, len(doc.Blocks))
	for i, block := range doc.Blocks {
		got[i] = goldenBlock{
			Seq:       block.Seq,
			Kind:      block.Kind,
			Text:      block.Text,
			ByteStart: block.Source.ByteStart,
			ByteEnd:   block.Source.ByteEnd,
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plain text differs from golden:\n got %#v\nwant %#v", got, want)
	}
	if doc.Format != domain.FormatText || doc.ID == "" || len(doc.Outline) != 1 {
		t.Fatalf("incomplete document IR: %#v", doc)
	}
}

func TestParserHandlesUTF8BOMAndRejectsBinaryOrInvalidUTF8(t *testing.T) {
	parser := NewParser()
	doc, err := parser.Parse(context.Background(), source.Input{Name: "bom.txt", R: bytes.NewReader([]byte("\xef\xbb\xbfHello.\n"))})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 1 || doc.Blocks[0].Text != "Hello." || doc.Blocks[0].Source.ByteStart != 3 {
		t.Fatalf("unexpected BOM result: %#v", doc.Blocks)
	}
	for _, data := range [][]byte{{'a', 0, 'b'}, {0xff, 0xfe, 'a'}} {
		if _, err := parser.Parse(context.Background(), source.Input{Name: "bad.txt", R: bytes.NewReader(data)}); err == nil {
			t.Fatalf("accepted invalid source bytes: %v", data)
		}
	}
}

func TestPlainBookInfersChaptersAndSkipsProbableTOC(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "plain", "chapters.txt")
	document, err := NewParser().Parse(context.Background(), source.Input{Name: path, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if document.Metadata.Title != "EXAMPLE BOOK" {
		t.Fatalf("title = %q, want title inferred from the opening line", document.Metadata.Title)
	}
	chapters := 0
	resetChapters := 0
	tocBlocks := 0
	for _, block := range document.Blocks {
		if block.SkipByDefault && contains(block.Semantics, "table_of_contents") {
			tocBlocks++
			continue
		}
		if block.Kind == domain.BlockHeading && strings.HasPrefix(strings.ToLower(block.Text), "chapter") {
			chapters++
			if block.Text == "CHAPTER 1 — A Reset Number" {
				resetChapters++
			}
		}
	}
	if chapters != 3 || resetChapters != 1 {
		t.Fatalf("chapter headings = %d, reset heading = %d, want 3 / 1: %#v", chapters, resetChapters, document.Blocks)
	}
	if tocBlocks != 3 {
		t.Fatalf("skipped %d TOC blocks, want label plus two entries", tocBlocks)
	}
	if !rangeInvariants(document.Outline, uint64(len(document.Blocks))) {
		t.Fatalf("outline ranges are invalid: %#v", document.Outline)
	}
	if len(document.Outline[0].Children) < 3 {
		t.Fatalf("part/chapter outline was not preserved: %#v", document.Outline)
	}
}

func TestPlainBookMatchesGolden(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, name := range []string{"chapters", "unlabeled_toc"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, "testdata", "plain", name+".txt")
			doc, err := NewParser().Parse(context.Background(), source.Input{Name: path, Path: path})
			if err != nil {
				t.Fatal(err)
			}
			actual, err := json.MarshalIndent(plainGoldenDocumentFrom(doc), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			actual = append(actual, '\n')
			goldenPath := filepath.Join(root, "testdata", "expected", "plain", name+".json")
			if os.Getenv("UPDATE_PLAIN_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			expected, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read plain golden: %v (set UPDATE_PLAIN_GOLDEN=1 after reviewing the parser change)", err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatal("plain-text document differs from golden; set UPDATE_PLAIN_GOLDEN=1 only after reviewing the parser change")
			}
		})
	}
}

func TestPlainTextDoesNotPromoteSentenceFragments(t *testing.T) {
	input := "A short sentence.\n\nAnother sentence without a title.\n"
	document, err := NewParser().Parse(context.Background(), source.Input{Name: "prose.txt", R: bytes.NewReader([]byte(input))})
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Blocks) != 2 {
		t.Fatalf("got %d blocks, want two paragraphs", len(document.Blocks))
	}
	for _, block := range document.Blocks {
		if block.Kind != domain.BlockParagraph {
			t.Fatalf("ordinary prose was promoted to %s: %#v", block.Kind, block)
		}
	}
}

func TestPlainTextClustersTitleCaseHeadingsAndKeepsSingleLineConservative(t *testing.T) {
	input := "A Possible Title\n\nThe Opening\n\nThe prose continues in a normal paragraph.\n\nA Second Thought\n\nMore narrative follows here.\n"
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "cluster.txt", R: strings.NewReader(input)})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Metadata.Title != "A Possible Title" {
		t.Fatalf("first title-case line title = %q", doc.Metadata.Title)
	}
	var titleCaseHeadings int
	for _, block := range doc.Blocks {
		if block.Kind == domain.BlockHeading && (block.Text == "The Opening" || block.Text == "A Second Thought") {
			titleCaseHeadings++
		}
	}
	if titleCaseHeadings != 2 {
		t.Fatalf("clustered title-case lines promoted %d headings, want two: %#v", titleCaseHeadings, doc.Blocks)
	}
}

func TestPlainTOCWithoutPageNumbersUsesRepeatedChapterPattern(t *testing.T) {
	input := "BOOK TITLE\n\nCONTENTS\n\nCHAPTER 1\n\nCHAPTER 2\n\nPART I\n\nCHAPTER 1\n\nThe real chapter text starts here."
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "toc.txt", R: strings.NewReader(input)})
	if err != nil {
		t.Fatal(err)
	}
	var skipped, chapterHeading, bodyParagraph int
	for _, block := range doc.Blocks {
		if contains(block.Semantics, "table_of_contents") {
			skipped++
		}
		if block.Kind == domain.BlockHeading && block.Text == "CHAPTER 1" {
			chapterHeading++
		}
		if block.Kind == domain.BlockParagraph && strings.Contains(block.Text, "The real chapter text") {
			bodyParagraph++
		}
	}
	if skipped != 3 {
		t.Fatalf("skipped %d contents blocks, want label and two chapter entries: %#v", skipped, doc.Blocks)
	}
	if chapterHeading != 1 || bodyParagraph != 1 {
		t.Fatalf("real chapter content was not preserved: %#v", doc.Blocks)
	}
}

func TestPlainTOCWithoutContentsLabelUsesDottedLeaders(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "plain", "unlabeled_toc.txt")
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: path, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	var skipped int
	for _, block := range doc.Blocks {
		if contains(block.Semantics, "table_of_contents") && block.SkipByDefault {
			skipped++
		}
	}
	if skipped != 3 {
		t.Fatalf("unlabeled TOC entries skipped = %d, want three: %#v", skipped, doc.Blocks)
	}
}

func TestPlainMatterPhraseAndChapterProseStayAsParagraphs(t *testing.T) {
	for _, input := range []string{
		"BOOK TITLE\n\nIntroduction to the meadow was a peaceful beginning\n",
		"BOOK TITLE\n\nCHAPTER 1\nThe story begins here\n",
	} {
		doc, err := NewParser().Parse(context.Background(), source.Input{Name: "conservative.txt", R: strings.NewReader(input)})
		if err != nil {
			t.Fatal(err)
		}
		for _, block := range doc.Blocks {
			if strings.Contains(block.Text, "Introduction to the meadow") || strings.Contains(block.Text, "The story begins here") {
				if block.Kind != domain.BlockParagraph {
					t.Errorf("prose was promoted to %s: %#v", block.Kind, block)
				}
			}
			if strings.Contains(block.Text, "CHAPTER 1 — The story begins here") {
				t.Errorf("chapter prose was absorbed into subtitle: %#v", block)
			}
		}
	}
}

func TestGutenbergBoilerplateIsRetainedButSkipped(t *testing.T) {
	input := "Publisher notes\n*** START OF THE PROJECT GUTENBERG EBOOK SAMPLE ***\n\nCHAPTER 1\n\nStory text.\n*** END OF THE PROJECT GUTENBERG EBOOK SAMPLE ***\nLicense text\n"
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "gutenberg.txt", R: strings.NewReader(input)})
	if err != nil {
		t.Fatal(err)
	}
	var boilerplate, story int
	for _, block := range doc.Blocks {
		if contains(block.Semantics, "gutenberg_boilerplate") {
			boilerplate++
			if !block.SkipByDefault {
				t.Errorf("Gutenberg boilerplate not skipped: %#v", block)
			}
		}
		if strings.Contains(block.Text, "Story text") && block.SkipByDefault {
			story++
		}
	}
	if boilerplate == 0 || story != 0 {
		t.Fatalf("boilerplate/story handling failed: %#v", doc.Blocks)
	}
}

func TestPlainStructureIDsAreStableAcrossRepeatedParsing(t *testing.T) {
	input := source.Input{Name: "book.txt", R: bytes.NewReader([]byte("BOOK TITLE\n\nCHAPTER 1\nThe Start\n\nA paragraph with enough prose."))}
	first, err := NewParser().Parse(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewParser().Parse(context.Background(), source.Input{Name: input.Name, R: bytes.NewReader([]byte("BOOK TITLE\n\nCHAPTER 1\nThe Start\n\nA paragraph with enough prose."))})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Blocks) != len(second.Blocks) || len(first.Outline) != len(second.Outline) {
		t.Fatal("repeated parse changed structure length")
	}
	for index := range first.Blocks {
		if first.Blocks[index].ID != second.Blocks[index].ID {
			t.Fatalf("block %d ID changed: %q != %q", index, first.Blocks[index].ID, second.Blocks[index].ID)
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func rangeInvariants(nodes []*domain.OutlineNode, blockCount uint64) bool {
	for _, node := range nodes {
		if node.StartBlock > node.EndBlockExclusive || node.EndBlockExclusive > blockCount {
			return false
		}
		for _, child := range node.Children {
			if child.StartBlock < node.StartBlock || child.EndBlockExclusive > node.EndBlockExclusive || !rangeInvariants([]*domain.OutlineNode{child}, blockCount) {
				return false
			}
		}
	}
	return true
}

type plainGoldenDocument struct {
	Format   domain.Format      `json:"format"`
	Metadata domain.Metadata    `json:"metadata"`
	Blocks   []plainGoldenBlock `json:"blocks"`
	Outline  []plainGoldenNode  `json:"outline"`
	Warnings []string           `json:"warnings"`
}

type plainGoldenBlock struct {
	Seq       uint64           `json:"seq"`
	Kind      domain.BlockKind `json:"kind"`
	Text      string           `json:"text"`
	Level     int              `json:"level,omitempty"`
	ByteStart int64            `json:"byte_start"`
	ByteEnd   int64            `json:"byte_end"`
	Semantics []string         `json:"semantics,omitempty"`
	Skip      bool             `json:"skip_by_default,omitempty"`
}

type plainGoldenNode struct {
	Title             string                 `json:"title"`
	Role              domain.OutlineRole     `json:"role"`
	Depth             int                    `json:"depth"`
	StartBlock        uint64                 `json:"start_block"`
	EndBlockExclusive uint64                 `json:"end_block_exclusive"`
	Confidence        domain.Confidence      `json:"confidence"`
	Provenance        domain.StructureSource `json:"provenance"`
	Children          []plainGoldenNode      `json:"children,omitempty"`
}

func plainGoldenDocumentFrom(doc *domain.Document) plainGoldenDocument {
	result := plainGoldenDocument{Format: doc.Format, Metadata: doc.Metadata, Warnings: doc.Warnings}
	for _, block := range doc.Blocks {
		result.Blocks = append(result.Blocks, plainGoldenBlock{Seq: block.Seq, Kind: block.Kind, Text: block.Text, Level: block.Level, ByteStart: block.Source.ByteStart, ByteEnd: block.Source.ByteEnd, Semantics: block.Semantics, Skip: block.SkipByDefault})
	}
	for _, node := range doc.Outline {
		result.Outline = append(result.Outline, plainGoldenNodeFrom(node))
	}
	return result
}

func plainGoldenNodeFrom(node *domain.OutlineNode) plainGoldenNode {
	result := plainGoldenNode{Title: node.Title, Role: node.Role, Depth: node.Depth, StartBlock: node.StartBlock, EndBlockExclusive: node.EndBlockExclusive, Confidence: node.Confidence, Provenance: node.Provenance}
	for _, child := range node.Children {
		result.Children = append(result.Children, plainGoldenNodeFrom(child))
	}
	return result
}

var _ source.Parser = Parser{}
