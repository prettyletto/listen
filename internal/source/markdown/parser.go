// Package markdown parses Markdown AST structure into the canonical Document IR.
package markdown

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
	textsource "github.com/prettyletto/listen/internal/source/text"
	"github.com/prettyletto/listen/internal/structure"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	textm "github.com/yuin/goldmark/text"
)

const parserVersion = "markdown-ast-v1"

type Parser struct{}

func NewParser() Parser { return Parser{} }

func (Parser) Name() string { return "Markdown" }

func (Parser) Probe(ctx context.Context, input source.Input) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	name := input.Name
	if filepath.Ext(name) == "" {
		name = input.Path
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return 100, nil
	default:
		return 0, nil
	}
}

func (Parser) Parse(ctx context.Context, input source.Input) (*domain.Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	content, err := readInput(ctx, input)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("%w; listen expects readable text", textsource.ErrBinarySource)
	}
	offset := 0
	if bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		content = content[3:]
		offset = 3
	}
	if !utf8.Valid(content) {
		return nil, textsource.ErrInvalidUTF8
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, textsource.ErrEmptySource
	}
	path := input.Path
	if path == "" {
		path = input.Name
	}
	if path != "" {
		if absolute, err := filepath.Abs(path); err == nil {
			path = absolute
		}
	}
	fallbackTitle := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if fallbackTitle == "" || fallbackTitle == "." {
		fallbackTitle = "Untitled document"
	}

	body, bodyOffset, metadata, frontMatter := extractFrontMatter(content)
	offset += bodyOffset
	explicitTitle := metadata.Title != ""
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM))
	root := parser.Parser().Parse(textm.NewReader(body))
	docID := stableID(parserVersion, hex.EncodeToString(sha256Sum(content)))
	doc := &domain.Document{ID: docID, Format: domain.FormatMarkdown, Metadata: metadata}
	occurrences := make(map[string]int)
	if metadata.Title == "" {
		doc.Metadata.Title = fallbackTitle
	}
	if len(frontMatter) > 0 {
		appendBlock(doc, occurrences, path, domain.BlockCode, string(frontMatter), 0, len(frontMatter), []string{"front_matter"}, true, offset-bodyOffset)
	}
	if err := collectBlocks(ctx, root, body, offset, path, doc, occurrences); err != nil {
		return nil, err
	}
	if len(doc.Blocks) == 0 || onlySkipped(doc.Blocks) {
		return nil, textsource.ErrEmptySource
	}
	if !explicitTitle {
		for _, block := range doc.Blocks {
			if block.Kind == domain.BlockHeading && block.Level == 1 {
				doc.Metadata.Title = block.Text
				break
			}
		}
	}
	doc.Outline = buildOutline(doc)
	return doc, nil
}

func collectBlocks(ctx context.Context, root ast.Node, sourceBytes []byte, sourceOffset int, path string, doc *domain.Document, occurrences map[string]int) error {
	return ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if err := ctx.Err(); err != nil {
			return ast.WalkStop, err
		}
		switch current := node.(type) {
		case *ast.Heading:
			text := inlineText(current, sourceBytes)
			start, end := blockRange(current, sourceBytes)
			appendBlock(doc, occurrences, path, domain.BlockHeading, text, start, end, nil, false, sourceOffset)
			block := &doc.Blocks[len(doc.Blocks)-1]
			block.Level = current.Level
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock:
			start, end := blockRange(current, sourceBytes)
			appendBlock(doc, occurrences, path, domain.BlockCode, lineText(current, sourceBytes), start, end, codeSemantics(current, sourceBytes), true, sourceOffset)
			return ast.WalkSkipChildren, nil
		case *ast.CodeBlock:
			start, end := blockRange(current, sourceBytes)
			appendBlock(doc, occurrences, path, domain.BlockCode, lineText(current, sourceBytes), start, end, nil, true, sourceOffset)
			return ast.WalkSkipChildren, nil
		case *ast.Paragraph, *ast.TextBlock:
			start, end := blockRange(current, sourceBytes)
			kind, semantics := enclosingKind(current)
			appendBlock(doc, occurrences, path, kind, inlineText(current, sourceBytes), start, end, semantics, false, sourceOffset)
			return ast.WalkSkipChildren, nil
		case *extensionast.TableRow:
			start, end := blockRange(current, sourceBytes)
			appendBlock(doc, occurrences, path, domain.BlockTableRow, tableCells(current, sourceBytes), start, end, []string{"markdown_table"}, false, sourceOffset)
			return ast.WalkSkipChildren, nil
		case *extensionast.TableHeader:
			start, end := blockRange(current, sourceBytes)
			appendBlock(doc, occurrences, path, domain.BlockTableRow, tableCells(current, sourceBytes), start, end, []string{"markdown_table", "table_header"}, false, sourceOffset)
			return ast.WalkSkipChildren, nil
		case *ast.ThematicBreak:
			start, end := blockRange(current, sourceBytes)
			appendBlock(doc, occurrences, path, domain.BlockBreak, "", start, end, nil, true, sourceOffset)
			return ast.WalkSkipChildren, nil
		default:
			return ast.WalkContinue, nil
		}
	})
}

func appendBlock(doc *domain.Document, occurrences map[string]int, path string, kind domain.BlockKind, text string, start, end int, semantics []string, skip bool, sourceOffset int) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" && kind != domain.BlockBreak {
		return
	}
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	hash := sha256Sum([]byte(text))
	key := hex.EncodeToString(hash)
	occurrence := occurrences[key]
	occurrences[key]++
	doc.Blocks = append(doc.Blocks, domain.Block{
		ID: stableID(parserVersion, path, key, fmt.Sprint(occurrence)), Seq: uint64(len(doc.Blocks)), Kind: kind,
		Text: text, Source: domain.SourceRef{Path: path, ByteStart: int64(start + sourceOffset), ByteEnd: int64(end + sourceOffset)},
		Semantics: append([]string(nil), semantics...), SkipByDefault: skip,
	})
}

func inlineText(node ast.Node, sourceBytes []byte) string {
	var result strings.Builder
	_ = ast.Walk(node, func(current ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch value := current.(type) {
		case *ast.Text:
			result.Write(value.Value(sourceBytes))
			if value.SoftLineBreak() || value.HardLineBreak() {
				result.WriteByte(' ')
			}
		case *ast.String:
			result.Write(value.Value)
		case *ast.RawHTML:
			// Raw HTML tags are structural markup, so do not read tag bytes aloud.
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return result.String()
}

func lineText(node ast.Node, sourceBytes []byte) string {
	if node.Lines() == nil {
		return ""
	}
	return string(node.Lines().Value(sourceBytes))
}

func blockRange(node ast.Node, sourceBytes []byte) (int, int) {
	lines := node.Lines()
	if lines == nil || lines.Len() == 0 {
		position := node.Pos()
		if position < 0 {
			position = 0
		}
		if position > len(sourceBytes) {
			position = len(sourceBytes)
		}
		start := bytes.LastIndex(sourceBytes[:position], []byte("\n")) + 1
		end := bytes.IndexByte(sourceBytes[position:], '\n')
		if end < 0 {
			end = len(sourceBytes)
		} else {
			end += position
		}
		return start, end
	}
	first := lines.At(0)
	last := lines.At(lines.Len() - 1)
	start, end := first.Start, last.Stop
	if start < 0 {
		start = 0
	}
	if end > len(sourceBytes) {
		end = len(sourceBytes)
	}
	return start, end
}

func tableCells(node ast.Node, sourceBytes []byte) string {
	var cells []string
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		if _, ok := child.(*extensionast.TableCell); ok {
			cells = append(cells, inlineText(child, sourceBytes))
		}
	}
	return strings.Join(cells, "; ")
}

func enclosingKind(node ast.Node) (domain.BlockKind, []string) {
	kind := domain.BlockParagraph
	var semantics []string
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.(type) {
		case *ast.ListItem:
			kind = domain.BlockListItem
		case *ast.Blockquote:
			if kind == domain.BlockParagraph {
				kind = domain.BlockQuote
			}
			semantics = append(semantics, "blockquote")
		}
	}
	return kind, semantics
}

func codeSemantics(node *ast.FencedCodeBlock, sourceBytes []byte) []string {
	if node.Info == nil {
		return nil
	}
	language := strings.TrimSpace(string(node.Info.Value(sourceBytes)))
	if language == "" {
		return nil
	}
	return []string{"language:" + language}
}

func buildOutline(doc *domain.Document) []*domain.OutlineNode {
	root := &domain.OutlineNode{ID: stableID(parserVersion, "book", doc.ID), Title: doc.Metadata.Title, Role: domain.RoleBook, Depth: 0, StartBlock: 0, EndBlockExclusive: uint64(len(doc.Blocks)), Confidence: domain.ConfidenceHigh, Provenance: domain.StructureHeading}
	minLevel := 7
	for _, block := range doc.Blocks {
		if block.Kind == domain.BlockHeading && block.Level > 0 && block.Level < minLevel {
			minLevel = block.Level
		}
	}
	if minLevel == 7 {
		return []*domain.OutlineNode{root}
	}
	var stack []*domain.OutlineNode
	stack = append(stack, root)
	for _, block := range doc.Blocks {
		if block.Kind != domain.BlockHeading || block.SkipByDefault {
			continue
		}
		level := block.Level - minLevel + 1
		if level < 1 {
			level = 1
		}
		for len(stack) > 1 && stack[len(stack)-1].Depth >= level {
			stack[len(stack)-1].EndBlockExclusive = block.Seq
			stack = stack[:len(stack)-1]
		}
		role := structure.ClassifyBlock(block)
		if role == domain.RoleUnknown && level == 1 && strings.EqualFold(block.Text, doc.Metadata.Title) {
			role = domain.RoleBook
		}
		parent := stack[len(stack)-1]
		node := &domain.OutlineNode{ID: stableID(parserVersion, "heading", block.ID), Title: block.Text, Role: role, Depth: level, StartBlock: block.Seq, EndBlockExclusive: uint64(len(doc.Blocks)), SourceTarget: block.Source, Confidence: domain.ConfidenceHigh, Provenance: domain.StructureHeading}
		parent.Children = append(parent.Children, node)
		stack = append(stack, node)
	}
	structure.ClassifyMajorUnits(root, doc.Blocks)
	return []*domain.OutlineNode{root}
}

func extractFrontMatter(content []byte) ([]byte, int, domain.Metadata, []byte) {
	var metadata domain.Metadata
	firstEnd := bytes.IndexByte(content, '\n')
	if firstEnd < 0 {
		return content, 0, metadata, nil
	}
	first := strings.TrimSpace(strings.TrimSuffix(string(content[:firstEnd]), "\r"))
	if first != "---" && first != "+++" {
		return content, 0, metadata, nil
	}
	closing := first
	offset := firstEnd + 1
	for offset <= len(content) {
		relEnd := bytes.IndexByte(content[offset:], '\n')
		end := len(content)
		if relEnd >= 0 {
			end = offset + relEnd
		}
		line := strings.TrimSpace(strings.TrimSuffix(string(content[offset:end]), "\r"))
		if line == closing {
			frontEnd := end
			bodyOffset := end
			if bodyOffset < len(content) && content[bodyOffset] == '\n' {
				bodyOffset++
			}
			metadata = parseFrontMatter(content[firstEnd+1 : offset])
			return content[bodyOffset:], bodyOffset, metadata, content[:frontEnd]
		}
		if relEnd < 0 {
			break
		}
		offset = end + 1
	}
	return content, 0, domain.Metadata{}, nil
}

func parseFrontMatter(content []byte) domain.Metadata {
	var metadata domain.Metadata
	var listKey string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "- ") && (listKey == "author" || listKey == "authors") {
			if author := unquoteScalar(strings.TrimSpace(strings.TrimPrefix(line, "- "))); author != "" {
				metadata.Authors = append(metadata.Authors, author)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			key, value, ok = strings.Cut(line, "=")
		}
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		listKey = ""
		switch key {
		case "title":
			metadata.Title = unquoteScalar(value)
		case "author", "authors":
			if value == "" {
				listKey = key
				continue
			}
			value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
			for _, author := range strings.Split(value, ",") {
				if author = unquoteScalar(author); author != "" {
					metadata.Authors = append(metadata.Authors, author)
				}
			}
		case "language", "lang":
			metadata.Language = unquoteScalar(value)
		}
	}
	return metadata
}

func unquoteScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		value = value[1 : len(value)-1]
	}
	return strings.TrimSpace(value)
}

func onlySkipped(blocks []domain.Block) bool {
	for _, block := range blocks {
		if !block.SkipByDefault && block.Kind != domain.BlockBreak {
			return false
		}
	}
	return true
}

func readInput(ctx context.Context, input source.Input) ([]byte, error) {
	reader := input.R
	var file *os.File
	if reader == nil {
		if input.Path == "" {
			return nil, errors.New("Markdown source has no reader or path")
		}
		opened, err := os.Open(input.Path)
		if err != nil {
			return nil, fmt.Errorf("open Markdown source: %w", err)
		}
		file, reader = opened, opened
	}
	if file != nil {
		defer file.Close()
	}
	var result bytes.Buffer
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := reader.Read(buffer)
		if n > 0 {
			_, _ = result.Write(buffer[:n])
		}
		if err == io.EOF {
			return result.Bytes(), nil
		}
		if err != nil {
			return nil, fmt.Errorf("read Markdown source: %w", err)
		}
	}
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func sha256Sum(content []byte) []byte {
	hash := sha256.Sum256(content)
	return hash[:]
}
