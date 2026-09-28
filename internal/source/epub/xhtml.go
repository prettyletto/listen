package epub

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/prettyletto/listen/internal/domain"
	"golang.org/x/net/html"
)

type contentFile struct {
	path      string
	index     int
	blocks    []domain.Block
	targets   map[string]uint64
	warnings  []string
	start     uint64
	end       uint64
	navBlocks int
	tocBlocks int
	tocItems  int
	tocTitle  bool
}

type nodeState struct {
	semantics  []string
	skip       bool
	note       bool
	navigation bool
	toc        bool
}

type blockExtractor struct {
	ctx         context.Context
	path        string
	spineIndex  int
	seq         uint64
	blocks      []domain.Block
	targets     map[string]uint64
	duplicates  []string
	occurrences map[string]int
	pendingIDs  []string
	navBlocks   int
	tocBlocks   int
	tocItems    int
	tocTitle    bool
}

func parseContentFile(ctx context.Context, name string, spineIndex int, startSeq uint64, data []byte) (contentFile, error) {
	if !utf8.Valid(data) {
		return contentFile{}, fmt.Errorf("EPUB XHTML %q is not valid UTF-8", name)
	}
	document, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return contentFile{}, fmt.Errorf("parse EPUB XHTML %q: %w", name, err)
	}
	extractor := &blockExtractor{
		ctx: ctx, path: name, spineIndex: spineIndex, seq: startSeq,
		targets: make(map[string]uint64), occurrences: make(map[string]int),
	}
	if err := extractor.walk(document, nodeState{}); err != nil {
		return contentFile{}, err
	}
	result := contentFile{
		path: name, index: spineIndex, blocks: extractor.blocks,
		targets: extractor.targets, navBlocks: extractor.navBlocks,
		tocBlocks: extractor.tocBlocks, tocItems: extractor.tocItems,
		tocTitle: extractor.tocTitle,
	}
	if len(result.blocks) != 0 {
		result.start = result.blocks[0].Seq
		result.end = result.blocks[len(result.blocks)-1].Seq + 1
		for _, block := range result.blocks {
			if !block.SkipByDefault && block.Kind != domain.BlockBreak {
				result.start = block.Seq
				break
			}
		}
	}
	if len(extractor.duplicates) > 0 {
		result.warnings = append(result.warnings, fmt.Sprintf("EPUB XHTML %q repeats anchor IDs: %s", name, strings.Join(extractor.duplicates, ", ")))
	}
	return result, nil
}

func (e *blockExtractor) walk(node *html.Node, inherited nodeState) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	if node.Type != html.ElementNode {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := e.walk(child, inherited); err != nil {
				return err
			}
		}
		return nil
	}
	if isHidden(node) || ignoredElement(node) || isPageBreak(node) {
		return nil
	}
	state := inherited
	state.semantics = append(append([]string(nil), inherited.semantics...), nodeSemantics(node)...)
	state.navigation = inherited.navigation || isNavigation(node)
	state.toc = inherited.toc || contains(nodeSemantics(node), "toc")
	state.skip = inherited.skip || state.navigation
	state.note = inherited.note || isNote(node)
	if id := attribute(node, "id"); id != "" {
		e.pendingIDs = append(e.pendingIDs, id)
	}
	if node.Data == "hr" {
		e.emit(domain.BlockBreak, "", 0, state, node)
		return nil
	}
	if state.note && isNoteBoundary(node) {
		text, ids, err := collectTextAndIDs(e.ctx, node, collectOptions{skipNestedLists: false})
		if err != nil {
			return err
		}
		e.pendingIDs = append(e.pendingIDs, ids...)
		e.emit(domain.BlockNote, text, 0, state, node)
		return nil
	}
	switch {
	case isHeading(node):
		text, ids, err := collectTextAndIDs(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		e.pendingIDs = append(e.pendingIDs, ids...)
		if isTOCTitle(text) {
			e.tocTitle = true
		}
		e.emit(domain.BlockHeading, text, headingLevel(node), state, node)
		return nil
	case node.Data == "p":
		text, ids, err := collectTextAndIDs(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		e.pendingIDs = append(e.pendingIDs, ids...)
		e.emit(domain.BlockParagraph, text, 0, state, node)
		return nil
	case node.Data == "pre":
		text, ids, err := collectTextAndIDs(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		e.pendingIDs = append(e.pendingIDs, ids...)
		state.skip = true
		state.semantics = append(state.semantics, "code")
		e.emit(domain.BlockCode, text, 0, state, node)
		return nil
	case node.Data == "blockquote":
		text, ids, err := collectTextAndIDs(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		e.pendingIDs = append(e.pendingIDs, ids...)
		e.emit(domain.BlockQuote, text, 0, state, node)
		return nil
	case node.Data == "li":
		text, ids, err := collectTextAndIDs(e.ctx, node, collectOptions{skipNestedLists: true})
		if err != nil {
			return err
		}
		e.pendingIDs = append(e.pendingIDs, ids...)
		if hasInternalLink(node) {
			state.semantics = append(state.semantics, "internal_link")
		}
		e.emit(domain.BlockListItem, text, 0, state, node)
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && (child.Data == "ol" || child.Data == "ul") {
				if err := e.walk(child, state); err != nil {
					return err
				}
			}
		}
		return nil
	case node.Data == "table":
		return e.walkTable(node, state)
	case node.Data == "caption":
		text, ids, err := collectTextAndIDs(e.ctx, node, collectOptions{})
		if err != nil {
			return err
		}
		e.pendingIDs = append(e.pendingIDs, ids...)
		e.emit(domain.BlockParagraph, text, 0, state, node)
		return nil
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := e.walk(child, state); err != nil {
			return err
		}
	}
	return nil
}

func (e *blockExtractor) walkTable(node *html.Node, state nodeState) error {
	var visit func(*html.Node) error
	visit = func(current *html.Node) error {
		if err := e.ctx.Err(); err != nil {
			return err
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && (isHidden(child) || ignoredElement(child) || isPageBreak(child)) {
				continue
			}
			if child.Type == html.ElementNode && child.Data == "caption" {
				if id := attribute(child, "id"); id != "" {
					e.pendingIDs = append(e.pendingIDs, id)
				}
				text, ids, err := collectTextAndIDs(e.ctx, child, collectOptions{})
				if err != nil {
					return err
				}
				e.pendingIDs = append(e.pendingIDs, ids...)
				e.emit(domain.BlockParagraph, text, 0, state, child)
				continue
			}
			if child.Type == html.ElementNode && child.Data == "tr" {
				if id := attribute(child, "id"); id != "" {
					e.pendingIDs = append(e.pendingIDs, id)
				}
				text, ids, err := collectTableRow(e.ctx, child)
				if err != nil {
					return err
				}
				e.pendingIDs = append(e.pendingIDs, ids...)
				e.emit(domain.BlockTableRow, text, 0, state, child)
				continue
			}
			if child.Type == html.ElementNode {
				if id := attribute(child, "id"); id != "" {
					e.pendingIDs = append(e.pendingIDs, id)
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if id := attribute(node, "id"); id != "" {
		e.pendingIDs = append(e.pendingIDs, id)
	}
	return visit(node)
}

func collectTableRow(ctx context.Context, row *html.Node) (string, []string, error) {
	var cells []string
	var ids []string
	for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
		if cell.Type != html.ElementNode || (cell.Data != "td" && cell.Data != "th") || isHidden(cell) {
			continue
		}
		text, nestedIDs, err := collectTextAndIDs(ctx, cell, collectOptions{})
		if err != nil {
			return "", nil, err
		}
		if text != "" {
			cells = append(cells, text)
		}
		ids = append(ids, nestedIDs...)
		if id := attribute(cell, "id"); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(cells, "; "), ids, nil
}

func (e *blockExtractor) emit(kind domain.BlockKind, text string, level int, state nodeState, node *html.Node) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" && kind != domain.BlockBreak {
		return
	}
	seq := e.seq
	e.seq++
	fragment := attribute(node, "id")
	identity := string(kind) + "\x00" + fragment + "\x00" + text
	occurrence := e.occurrences[identity]
	e.occurrences[identity] = occurrence + 1
	block := domain.Block{
		ID:            stableBlockID(e.path, fragment, fmt.Sprint(occurrence), string(kind), text),
		Seq:           seq,
		Kind:          kind,
		Text:          text,
		Level:         level,
		Semantics:     uniqueStrings(state.semantics),
		SkipByDefault: state.skip || state.note,
		Source: domain.SourceRef{
			Path: e.path, Fragment: fragment, SpineIndex: e.spineIndex,
		},
	}
	e.blocks = append(e.blocks, block)
	if state.navigation {
		e.navBlocks++
	}
	if state.toc {
		e.tocBlocks++
	}
	if kind == domain.BlockListItem && contains(state.semantics, "internal_link") {
		e.tocItems++
	}
	if kind == domain.BlockHeading && isTOCTitle(text) {
		e.tocTitle = true
	}
	seenPending := make(map[string]bool, len(e.pendingIDs))
	for _, id := range e.pendingIDs {
		if id == "" {
			continue
		}
		if seenPending[id] {
			continue
		}
		seenPending[id] = true
		if _, exists := e.targets[id]; exists {
			e.duplicates = appendUnique(e.duplicates, id)
			continue
		}
		e.targets[id] = seq
	}
	e.pendingIDs = nil
}

func stableBlockID(path, fragment, seq, kind, text string) string {
	return epubID("epub-block-v1", path, fragment, seq, kind, text)
}

type collectOptions struct {
	skipNestedLists bool
}

func collectTextAndIDs(ctx context.Context, node *html.Node, options collectOptions) (string, []string, error) {
	var text strings.Builder
	var ids []string
	root := node
	var walk func(*html.Node) error
	walk = func(current *html.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if current.Type == html.TextNode {
			text.WriteString(current.Data)
			text.WriteByte(' ')
			return nil
		}
		if current.Type != html.ElementNode {
			for child := current.FirstChild; child != nil; child = child.NextSibling {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}
		if isHidden(current) || ignoredElement(current) || isPageBreak(current) || isNoteref(current) || (current != root && isNote(current)) {
			return nil
		}
		if options.skipNestedLists && (current.Data == "ol" || current.Data == "ul") {
			return nil
		}
		if current != root {
			if id := attribute(current, "id"); id != "" {
				ids = append(ids, id)
			}
		}
		if current.Data == "img" {
			alt := strings.TrimSpace(attribute(current, "alt"))
			if alt != "" && !isDecorative(current) {
				text.WriteString(alt)
				text.WriteByte(' ')
			}
			return nil
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(node); err != nil {
		return "", nil, err
	}
	return strings.Join(strings.Fields(text.String()), " "), ids, nil
}

func ignoredElement(node *html.Node) bool {
	switch node.Data {
	case "head", "script", "style", "template", "title", "noscript":
		return true
	default:
		return false
	}
}

func isHidden(node *html.Node) bool {
	if hasAttribute(node, "hidden") || strings.EqualFold(attribute(node, "aria-hidden"), "true") {
		return true
	}
	style := strings.ToLower(strings.ReplaceAll(attribute(node, "style"), " ", ""))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden")
}

func isPageBreak(node *html.Node) bool {
	for _, semantic := range nodeSemantics(node) {
		if semantic == "pagebreak" || semantic == "doc-pagebreak" {
			return true
		}
	}
	for _, className := range strings.Fields(strings.ToLower(attribute(node, "class"))) {
		if className == "pagebreak" || className == "page-break" || className == "page_break" {
			return true
		}
	}
	return false
}

func isNavigation(node *html.Node) bool {
	if node.Data != "nav" {
		return false
	}
	return true
}

func isNote(node *html.Node) bool {
	for _, semantic := range nodeSemantics(node) {
		switch semantic {
		case "footnote", "endnote", "doc-footnote", "doc-endnote":
			return true
		}
	}
	return false
}

func isNoteBoundary(node *html.Node) bool {
	switch node.Data {
	case "aside", "section", "div", "li", "p", "pre", "blockquote", "table", "caption", "tr":
		return isNote(node)
	default:
		return isHeading(node) && isNote(node)
	}
}

func isNoteref(node *html.Node) bool {
	for _, semantic := range nodeSemantics(node) {
		if semantic == "noteref" || semantic == "doc-noteref" {
			return true
		}
	}
	return strings.EqualFold(attribute(node, "role"), "doc-noteref")
}

func isDecorative(node *html.Node) bool {
	return strings.EqualFold(attribute(node, "role"), "presentation") || strings.EqualFold(attribute(node, "aria-hidden"), "true")
}

func hasInternalLink(node *html.Node) bool {
	if node.Type == html.ElementNode && node.Data == "a" {
		href := strings.TrimSpace(attribute(node, "href"))
		if href != "" {
			if reference, err := url.Parse(href); err == nil && !reference.IsAbs() && reference.Host == "" {
				return true
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if hasInternalLink(child) {
			return true
		}
	}
	return false
}

func isTOCTitle(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	for _, candidate := range []string{"contents", "table of contents", "content", "sumário", "sumario", "índice", "indice"} {
		if title == candidate || strings.HasPrefix(title, candidate+" ") {
			return true
		}
	}
	return false
}

func suppressTOCPage(file *contentFile, navPath string) bool {
	readable := 0
	for _, block := range file.blocks {
		if block.Kind != domain.BlockBreak {
			readable++
		}
	}
	if readable == 0 {
		return false
	}
	dominantSemanticTOC := file.tocBlocks > 0 && file.tocBlocks*2 >= readable
	dominantNavigationDocument := file.path == navPath && file.navBlocks > 0 && file.navBlocks*2 >= readable
	denseLinkedTOC := file.tocTitle && file.tocItems >= 3 && file.tocItems*2 >= readable
	if !dominantSemanticTOC && !dominantNavigationDocument && !denseLinkedTOC {
		return false
	}
	for index := range file.blocks {
		file.blocks[index].SkipByDefault = true
	}
	return true
}

func isHeading(node *html.Node) bool {
	return len(node.Data) == 2 && node.Data[0] == 'h' && node.Data[1] >= '1' && node.Data[1] <= '6'
}

func headingLevel(node *html.Node) int {
	if !isHeading(node) {
		return 0
	}
	return int(node.Data[1] - '0')
}

func nodeSemantics(node *html.Node) []string {
	var result []string
	for _, attr := range node.Attr {
		key := strings.ToLower(attr.Key)
		if key != "epub:type" && key != "role" {
			continue
		}
		for _, token := range strings.Fields(strings.ToLower(attr.Val)) {
			result = append(result, token)
			if prefix, local, ok := strings.Cut(token, ":"); ok && prefix != "" && local != "" {
				result = append(result, local)
			}
		}
	}
	return uniqueStrings(result)
}

func attribute(node *html.Node, name string) string {
	name = strings.ToLower(name)
	for _, attr := range node.Attr {
		if strings.ToLower(attr.Key) == name {
			return attr.Val
		}
	}
	return ""
}

func hasAttribute(node *html.Node, name string) bool {
	name = strings.ToLower(name)
	for _, attr := range node.Attr {
		if strings.ToLower(attr.Key) == name {
			return true
		}
	}
	return false
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func uniqueStrings(values []string) []string {
	var result []string
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "" {
			result = appendUnique(result, value)
		}
	}
	return result
}

func contains(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}
