package html

import (
	"context"
	"fmt"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
	htmlnode "golang.org/x/net/html"
)

type blockExtractor struct {
	ctx         context.Context
	path        string
	selected    *htmlnode.Node
	blocks      []domain.Block
	occurrences map[string]int
}

type nodeState struct {
	semantics          []string
	inContentContainer bool
	skip               bool
}

func extractBlocks(ctx context.Context, selected *htmlnode.Node, path string) ([]domain.Block, error) {
	extractor := &blockExtractor{ctx: ctx, path: path, selected: selected, occurrences: make(map[string]int)}
	if err := extractor.walk(selected, nodeState{}); err != nil {
		return nil, err
	}
	return extractor.blocks, nil
}

func (e *blockExtractor) walk(node *htmlnode.Node, inherited nodeState) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if node.Type != htmlnode.ElementNode {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := e.walk(child, inherited); err != nil {
				return err
			}
		}
		return nil
	}
	if isHidden(node) || ignoredElement(node) || isSidebarLike(node) || isChrome(node, e.selected, inherited.inContentContainer) {
		return nil
	}
	state := nodeState{
		semantics:          uniqueStrings(append(append([]string(nil), inherited.semantics...), elementSemantics(node)...)),
		inContentContainer: inherited.inContentContainer || isContentContainer(node),
		skip:               inherited.skip,
	}
	if isHeading(node) {
		text, err := collectText(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		e.emit(domain.BlockHeading, text, headingLevel(node), state, node)
		return nil
	}
	switch node.Data {
	case "p":
		text, err := collectText(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		e.emit(domain.BlockParagraph, text, 0, state, node)
		return nil
	case "pre":
		text, err := collectText(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		state.skip = true
		state.semantics = uniqueStrings(append(append(state.semantics, "code"), codeLanguage(node)...))
		e.emit(domain.BlockCode, text, 0, state, node)
		return nil
	case "blockquote":
		text, err := collectText(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		state.semantics = uniqueStrings(append(state.semantics, "blockquote"))
		e.emit(domain.BlockQuote, text, 0, state, node)
		return nil
	case "li":
		text, err := collectText(e.ctx, node, collectOptions{skipNestedLists: true})
		if err != nil {
			return err
		}
		e.emit(domain.BlockListItem, text, 0, state, node)
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == htmlnode.ElementNode && (child.Data == "ol" || child.Data == "ul") {
				if err := e.walk(child, state); err != nil {
					return err
				}
			}
		}
		return nil
	case "table":
		state.semantics = uniqueStrings(append(state.semantics, "table"))
		return e.walkTable(node, state)
	case "caption":
		text, err := collectText(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		state.semantics = uniqueStrings(append(state.semantics, "table_caption"))
		e.emit(domain.BlockParagraph, text, 0, state, node)
		return nil
	case "hr":
		state.skip = true
		e.emit(domain.BlockBreak, "", 0, state, node)
		return nil
	case "ol":
		state.semantics = uniqueStrings(append(state.semantics, "ordered_list"))
	case "ul":
		state.semantics = uniqueStrings(append(state.semantics, "unordered_list"))
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := e.walk(child, state); err != nil {
			return err
		}
	}
	return nil
}

func (e *blockExtractor) walkTable(table *htmlnode.Node, state nodeState) error {
	var visit func(*htmlnode.Node) error
	visit = func(node *htmlnode.Node) error {
		if err := e.ctx.Err(); err != nil {
			return err
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == htmlnode.ElementNode && (isHidden(child) || ignoredElement(child) || isSidebarLike(child)) {
				continue
			}
			if child.Type == htmlnode.ElementNode && child.Data == "caption" {
				text, err := collectText(e.ctx, child, collectOptions{})
				if err != nil {
					return err
				}
				semantics := uniqueStrings(append(append([]string(nil), state.semantics...), "table_caption"))
				e.emit(domain.BlockParagraph, text, 0, nodeState{semantics: semantics}, child)
				continue
			}
			if child.Type == htmlnode.ElementNode && child.Data == "tr" {
				text, header, err := collectTableRow(e.ctx, child)
				if err != nil {
					return err
				}
				semantics := append(append([]string(nil), state.semantics...), "table_row")
				if header {
					semantics = append(semantics, "table_header")
				}
				e.emit(domain.BlockTableRow, text, 0, nodeState{semantics: uniqueStrings(semantics)}, child)
				continue
			}
			if child.Type == htmlnode.ElementNode {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(table)
}

func collectTableRow(ctx context.Context, row *htmlnode.Node) (string, bool, error) {
	var cells []string
	header := false
	for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		if cell.Type != htmlnode.ElementNode || (cell.Data != "td" && cell.Data != "th") || isHidden(cell) {
			continue
		}
		if cell.Data == "th" {
			header = true
		}
		text, err := collectText(ctx, cell, collectOptions{})
		if err != nil {
			return "", false, err
		}
		if text != "" {
			cells = append(cells, text)
		}
	}
	return strings.Join(cells, "; "), header, nil
}

func (e *blockExtractor) emit(kind domain.BlockKind, text string, level int, state nodeState, node *htmlnode.Node) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" && kind != domain.BlockBreak {
		return
	}
	identity := string(kind) + "\x00" + text
	occurrence := e.occurrences[identity]
	e.occurrences[identity] = occurrence + 1
	blockID := stableID(parserVersion, e.path, identity, fmt.Sprint(occurrence))
	e.blocks = append(e.blocks, domain.Block{
		ID: blockID, Seq: uint64(len(e.blocks)), Kind: kind, Text: text, Level: level,
		Semantics: uniqueStrings(state.semantics), SkipByDefault: state.skip,
		Source: domain.SourceRef{Path: e.path, Fragment: attribute(node, "id"), DOMPath: domPath(node), SpineIndex: -1},
	})
}

type collectOptions struct {
	skipNestedLists bool
}

func collectText(ctx context.Context, root *htmlnode.Node, options collectOptions) (string, error) {
	var text strings.Builder
	var walk func(*htmlnode.Node) error
	walk = func(node *htmlnode.Node) error {
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
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}
		if isHidden(node) || ignoredElement(node) || isSuppressedTag(node) || isSidebarLike(node) || isNoteref(node) {
			return nil
		}
		if options.skipNestedLists && node != root && (node.Data == "ol" || node.Data == "ul") {
			return nil
		}
		if node.Data == "img" {
			if !isDecorative(node) {
				text.WriteString(attribute(node, "alt"))
				text.WriteByte(' ')
			}
			return nil
		}
		if node.Data == "br" {
			text.WriteByte(' ')
			return nil
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return "", err
	}
	return strings.Join(strings.Fields(text.String()), " "), nil
}
