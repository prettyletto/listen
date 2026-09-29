package gocode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
)

func TestNarrationFixtureMatchesGolden(t *testing.T) {
	path := filepath.Join("..", "..", "..", "testdata", "go", "narration.go")
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "narration.go", Path: path})
	if err != nil {
		t.Fatalf("parse Go fixture: %v", err)
	}
	if doc.Format != domain.FormatCode || doc.Metadata.Title != "narration" {
		t.Fatalf("unexpected document identity: format=%q metadata=%#v", doc.Format, doc.Metadata)
	}
	actual, err := json.MarshalIndent(goldenDocumentFrom(doc), "", "  ")
	if err != nil {
		t.Fatalf("encode Go narration golden: %v", err)
	}
	actual = append(actual, '\n')
	goldenPath := filepath.Join("..", "..", "..", "testdata", "expected", "go", "narration.json")
	if os.Getenv("UPDATE_GOCODE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(goldenPath, actual, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	expected, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %q: %v (set UPDATE_GOCODE_GOLDEN=1 only after reviewing the narration)", goldenPath, err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("Go narration differs from %s; set UPDATE_GOCODE_GOLDEN=1 only after reviewing the parser change", goldenPath)
	}
}

func TestNarrationUsesCommentsSignaturesAndControlFlow(t *testing.T) {
	const sourceText = "// Package sample demonstrates a reader.\n" +
		"package sample\n" +
		"import \"io\"\n" +
		"// Item stores one name.\n" +
		"type Item struct { Name string }\n" +
		"// Read returns the input unchanged.\n" +
		"func Read(input io.Reader) (Item, error) {\n" +
		"    if input == nil { return Item{}, nil }\n" +
		"    return Item{}, nil\n" +
		"}\n"
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "sample.go", R: strings.NewReader(sourceText)})
	if err != nil {
		t.Fatalf("parse Go source: %v", err)
	}
	var narration []string
	for _, block := range doc.Blocks {
		narration = append(narration, block.Text)
	}
	joined := strings.Join(narration, "\n")
	for _, expected := range []string{
		"Package sample",
		"Imports io.",
		"Item stores one name.",
		"Type item is a struct with fields name as a string.",
		"Function read accepts input as a reader type from io. It returns an item and an error.",
		"If input equals nil, it returns a value of type item and nil.",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("narration is missing %q:\n%s", expected, joined)
		}
	}
	for _, sourceSyntax := range []string{"func Read(", "input == nil", "io.Reader"} {
		if strings.Contains(joined, sourceSyntax) {
			t.Errorf("source syntax reached narration (%q):\n%s", sourceSyntax, joined)
		}
	}
}

func TestParserProbeRequiresGoExtensionAndParseRejectsOtherFormats(t *testing.T) {
	parser := NewParser()
	for _, test := range []struct {
		name string
		want int
	}{
		{name: "main.go", want: 100},
		{name: "MAIN.GO", want: 100},
		{name: "main.txt", want: 0},
	} {
		score, err := parser.Probe(context.Background(), source.Input{Name: test.name})
		if err != nil || score != test.want {
			t.Errorf("Probe(%q) = %d, %v; want %d, nil", test.name, score, err, test.want)
		}
	}
	if _, err := parser.Parse(context.Background(), source.Input{Name: "main.txt", R: strings.NewReader("plain text")}); !errors.Is(err, ErrNotGoSource) {
		t.Fatalf("Parse non-Go file = %v, want ErrNotGoSource", err)
	}
}

func TestParserRejectsInvalidGoAndHonorsCancellation(t *testing.T) {
	_, err := NewParser().Parse(context.Background(), source.Input{Name: "broken.go", R: strings.NewReader("package broken\nfunc (")})
	if !errors.Is(err, ErrInvalidGoSource) {
		t.Fatalf("invalid Go error = %v, want ErrInvalidGoSource", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewParser().Parse(ctx, source.Input{Name: "main.go", R: strings.NewReader("package main")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parse error = %v, want context.Canceled", err)
	}
}

func TestMethodTitlesAndOutlineIDsIncludeReceiverType(t *testing.T) {
	const sourceText = "package sample\ntype Entry struct{}\ntype Stream struct{}\n" +
		"func (Entry) Close() {}\nfunc (*Stream) Close() {}\n"
	doc, err := NewParser().Parse(context.Background(), source.Input{Name: "sample.go", R: strings.NewReader(sourceText)})
	if err != nil {
		t.Fatalf("parse Go source: %v", err)
	}
	var methods []*domain.OutlineNode
	for _, child := range doc.Outline[0].Children {
		if strings.HasPrefix(child.Title, "Method close on ") {
			methods = append(methods, child)
		}
	}
	if len(methods) != 2 {
		t.Fatalf("method outline = %#v, want two receiver-qualified close methods", methods)
	}
	if methods[0].Title == methods[1].Title {
		t.Fatalf("method titles collide: %q", methods[0].Title)
	}
	if methods[0].ID == methods[1].ID {
		t.Fatalf("method outline IDs collide: %q", methods[0].ID)
	}
}

func TestBodyNarrationChecksContextDuringTraversal(t *testing.T) {
	ctx := &cancelOnErrContext{Context: context.Background(), cancelAt: 3}
	body := &ast.BlockStmt{List: []ast.Stmt{&ast.ReturnStmt{}, &ast.ReturnStmt{}, &ast.ReturnStmt{}}}
	if _, err := bodyNarration(ctx, body); !errors.Is(err, context.Canceled) {
		t.Fatalf("body narration error = %v, want context.Canceled", err)
	}
}

type cancelOnErrContext struct {
	context.Context
	checks   int
	cancelAt int
}

func (ctx *cancelOnErrContext) Err() error {
	ctx.checks++
	if ctx.checks >= ctx.cancelAt {
		return context.Canceled
	}
	return ctx.Context.Err()
}

func TestParserEnforcesSourceAndASTLimits(t *testing.T) {
	oversized := make([]byte, MaxSourceBytes+1)
	if _, err := NewParser().Parse(context.Background(), source.Input{Name: "large.go", R: bytes.NewReader(oversized)}); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("oversized source error = %v, want ErrSourceTooLarge", err)
	}

	file := &ast.File{Decls: make([]ast.Decl, MaxASTNodes)}
	for index := range file.Decls {
		file.Decls[index] = &ast.BadDecl{}
	}
	if err := countASTNodes(context.Background(), file); !errors.Is(err, ErrSourceTooComplex) {
		t.Fatalf("oversized AST error = %v, want ErrSourceTooComplex", err)
	}
}

func TestParserProducesStableDocumentAndBlockIDs(t *testing.T) {
	content := "package sample\nfunc Run() {}\n"
	first, err := NewParser().Parse(context.Background(), source.Input{Name: "sample.go", R: strings.NewReader(content)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewParser().Parse(context.Background(), source.Input{Name: "sample.go", R: strings.NewReader(content)})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(first.Blocks) != len(second.Blocks) {
		t.Fatalf("narration identity changed across parses: %#v %#v", first, second)
	}
	for index := range first.Blocks {
		if first.Blocks[index].ID != second.Blocks[index].ID {
			t.Fatalf("block %d ID changed across parses", index)
		}
	}
}

type goldenDocument struct {
	Format   domain.Format   `json:"format"`
	Metadata domain.Metadata `json:"metadata"`
	Warnings []string        `json:"warnings"`
	Blocks   []goldenBlock   `json:"blocks"`
	Outline  []goldenNode    `json:"outline"`
}

type goldenBlock struct {
	Seq       uint64           `json:"seq"`
	Kind      domain.BlockKind `json:"kind"`
	Text      string           `json:"text"`
	Level     int              `json:"level"`
	ByteStart int64            `json:"byte_start"`
	ByteEnd   int64            `json:"byte_end"`
	Semantics []string         `json:"semantics,omitempty"`
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
		result.Blocks = append(result.Blocks, goldenBlock{
			Seq: block.Seq, Kind: block.Kind, Text: block.Text, Level: block.Level,
			ByteStart: block.Source.ByteStart, ByteEnd: block.Source.ByteEnd, Semantics: block.Semantics,
		})
	}
	for _, node := range doc.Outline {
		result.Outline = append(result.Outline, goldenNodeFrom(node))
	}
	return result
}

func goldenNodeFrom(node *domain.OutlineNode) goldenNode {
	result := goldenNode{
		Title: node.Title, Role: node.Role, Depth: node.Depth, StartBlock: node.StartBlock,
		EndBlockExclusive: node.EndBlockExclusive, Confidence: node.Confidence, Provenance: node.Provenance,
	}
	for _, child := range node.Children {
		result.Children = append(result.Children, goldenNodeFrom(child))
	}
	return result
}
