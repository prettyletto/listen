// Package html converts generic web pages into the canonical document IR.
// EPUB XHTML deliberately uses its separate parser and structure rules.
package html

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
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/source"
	textsource "github.com/prettyletto/listen/internal/source/text"
	htmlnode "golang.org/x/net/html"
)

const parserVersion = "generic-html-dom-v1"

type Parser struct{}

func NewParser() Parser { return Parser{} }

func (Parser) Name() string { return "HTML" }

func (Parser) Probe(ctx context.Context, input source.Input) (int, error) {
	if ctx == nil {
		return 0, errors.New("HTML parser requires a context")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	name := input.Name
	if filepath.Ext(name) == "" {
		name = input.Path
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm", ".xhtml":
		return 100, nil
	default:
		return 0, nil
	}
}

func (Parser) Parse(ctx context.Context, input source.Input) (*domain.Document, error) {
	if ctx == nil {
		return nil, errors.New("HTML parser requires a context")
	}
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
	if bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		content = content[3:]
	}
	if !utf8.Valid(content) {
		return nil, textsource.ErrInvalidUTF8
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil, textsource.ErrEmptySource
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	document, err := htmlnode.Parse(contextReader{ctx: ctx, reader: bytes.NewReader(content)})
	if err != nil {
		return nil, fmt.Errorf("parse HTML document: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	path := sourcePath(input)
	metadata := readMetadata(ctx, document)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, selectedKind, blocks, err := extractMainContent(ctx, document, path)
	if err != nil {
		return nil, err
	}
	if !hasNarratableBlock(blocks) {
		return nil, textsource.ErrEmptySource
	}
	if metadata.Title == "" {
		metadata.Title = firstHeading(blocks)
	}
	if metadata.Title == "" {
		metadata.Title = fallbackTitle(path)
	}
	outline := buildOutline(metadata.Title, blocks)

	contentHash := sha256.Sum256(content)
	docHash := sha256.Sum256(append([]byte(parserVersion+"\x00"), contentHash[:]...))
	var warnings []string
	switch selectedKind {
	case "body":
		warnings = append(warnings, "No <main> or dominant <article> was found; HTML body content was used with common site chrome suppressed.")
	case "article_after_empty_main":
		warnings = append(warnings, "The HTML <main> element had no readable text; a dominant <article> was used instead.")
	case "body_empty_main":
		warnings = append(warnings, "The HTML <main> element had no readable text and no article was selected; body content was used with common site chrome suppressed.")
	}
	return &domain.Document{
		ID: hex.EncodeToString(docHash[:]), Format: domain.FormatHTML,
		Metadata: metadata, Blocks: blocks, Outline: outline, Warnings: warnings,
	}, nil
}

type candidate struct {
	node  *htmlnode.Node
	words int
}

func extractMainContent(ctx context.Context, document *htmlnode.Node, path string) (*htmlnode.Node, string, []domain.Block, error) {
	mains, err := findCandidates(ctx, document, "main")
	if err != nil {
		return nil, "", nil, err
	}
	for _, item := range mains {
		blocks, err := extractBlocks(ctx, item.node, path)
		if err != nil {
			return nil, "", nil, err
		}
		if hasNarratableBlock(blocks) {
			return item.node, "main", blocks, nil
		}
	}

	articles, err := findCandidates(ctx, document, "article")
	if err != nil {
		return nil, "", nil, err
	}
	if len(articles) > 0 && dominantArticle(articles) {
		blocks, err := extractBlocks(ctx, articles[0].node, path)
		if err != nil {
			return nil, "", nil, err
		}
		if hasNarratableBlock(blocks) {
			kind := "article"
			if len(mains) > 0 {
				kind = "article_after_empty_main"
			}
			return articles[0].node, kind, blocks, nil
		}
	}

	body := findElement(document, "body")
	if body == nil {
		body = document
	}
	blocks, err := extractBlocks(ctx, body, path)
	if err != nil {
		return nil, "", nil, err
	}
	if len(mains) > 0 {
		return body, "body_empty_main", blocks, nil
	}
	return body, "body", blocks, nil
}

func findCandidates(ctx context.Context, root *htmlnode.Node, tag string) ([]candidate, error) {
	var result []candidate
	var walk func(*htmlnode.Node) error
	walk = func(node *htmlnode.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.Type == htmlnode.ElementNode {
			if isHidden(node) || ignoredElement(node) || isSidebarLike(node) {
				return nil
			}
			if node.Data == tag {
				words, err := visibleWordCount(ctx, node, node)
				if err != nil {
					return err
				}
				result = append(result, candidate{node: node, words: words})
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].words > result[j].words })
	return result, nil
}

func dominantArticle(articles []candidate) bool {
	if len(articles) == 1 {
		return articles[0].words > 0
	}
	if len(articles) < 2 {
		return false
	}
	largest, next := articles[0].words, articles[1].words
	return largest > next && largest*2 >= next*3
}

func visibleWordCount(ctx context.Context, root, selectedRoot *htmlnode.Node) (int, error) {
	var text strings.Builder
	var walk func(*htmlnode.Node, bool) error
	walk = func(node *htmlnode.Node, inContentContainer bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.Type == htmlnode.TextNode {
			text.WriteString(node.Data)
			text.WriteByte(' ')
			return nil
		}
		if node.Type != htmlnode.ElementNode {
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if err := walk(child, inContentContainer); err != nil {
					return err
				}
			}
			return nil
		}
		if isHidden(node) || ignoredElement(node) || isSidebarLike(node) || isChrome(node, selectedRoot, inContentContainer) {
			return nil
		}
		if node != root && node.Data == "article" {
			return nil
		}
		inContentContainer = inContentContainer || isContentContainer(node)
		if node.Data == "img" {
			if !isDecorative(node) {
				text.WriteString(attribute(node, "alt"))
				text.WriteByte(' ')
			}
			return nil
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child, inContentContainer); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, isContentContainer(root)); err != nil {
		return 0, err
	}
	return len(strings.Fields(text.String())), nil
}

func sourcePath(input source.Input) string {
	path := input.Path
	if path == "" {
		path = input.Name
	}
	if path != "" {
		if absolute, err := filepath.Abs(path); err == nil {
			return absolute
		}
	}
	return path
}

func readInput(ctx context.Context, input source.Input) ([]byte, error) {
	reader := input.R
	var file *os.File
	if reader == nil {
		if input.Path == "" {
			return nil, errors.New("HTML source has no reader or path")
		}
		opened, err := os.Open(input.Path)
		if err != nil {
			return nil, fmt.Errorf("open HTML source: %w", err)
		}
		file = opened
		reader = opened
	}
	if file != nil {
		defer file.Close()
	}
	content, err := io.ReadAll(contextReader{ctx: ctx, reader: reader})
	if err != nil {
		return nil, fmt.Errorf("read HTML source: %w", err)
	}
	return content, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

var _ source.Parser = Parser{}
