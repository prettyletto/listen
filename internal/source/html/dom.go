package html

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/structure"
	htmlnode "golang.org/x/net/html"
)

func readMetadata(ctx context.Context, document *htmlnode.Node) domain.Metadata {
	metadata := domain.Metadata{Title: "", Language: ""}
	var authors []string
	var walk func(*htmlnode.Node)
	walk = func(node *htmlnode.Node) {
		if ctx.Err() != nil {
			return
		}
		if node.Type == htmlnode.ElementNode {
			switch node.Data {
			case "html":
				if metadata.Language == "" {
					metadata.Language = strings.TrimSpace(attribute(node, "lang"))
				}
			case "title":
				if metadata.Title == "" {
					metadata.Title = strings.TrimSpace(nodeText(node))
				}
			case "meta":
				name := strings.ToLower(strings.TrimSpace(attribute(node, "name")))
				property := strings.ToLower(strings.TrimSpace(attribute(node, "property")))
				content := strings.TrimSpace(attribute(node, "content"))
				switch {
				case name == "author" && content != "":
					authors = append(authors, content)
				case metadata.Title == "" && (name == "title" || property == "og:title") && content != "":
					metadata.Title = content
				case metadata.Language == "" && strings.EqualFold(attribute(node, "http-equiv"), "content-language") && content != "":
					metadata.Language = content
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	metadata.Authors = uniqueAuthors(authors)
	return metadata
}

func nodeText(node *htmlnode.Node) string {
	var text strings.Builder
	var walk func(*htmlnode.Node)
	walk = func(current *htmlnode.Node) {
		if current.Type == htmlnode.TextNode {
			text.WriteString(current.Data)
			text.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(text.String()), " ")
}

func buildOutline(title string, blocks []domain.Block) []*domain.OutlineNode {
	root := &domain.OutlineNode{
		ID: stableID(parserVersion, "book", title), Title: title, Role: domain.RoleBook,
		Depth: 0, StartBlock: 0, EndBlockExclusive: uint64(len(blocks)),
		Confidence: domain.ConfidenceHigh, Provenance: domain.StructureHeading,
	}
	minLevel := 7
	for _, block := range blocks {
		if block.Kind == domain.BlockHeading && block.Level > 0 && block.Level < minLevel {
			minLevel = block.Level
		}
	}
	if minLevel == 7 {
		return []*domain.OutlineNode{root}
	}
	stack := []*domain.OutlineNode{root}
	for _, block := range blocks {
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
		if role == domain.RoleUnknown {
			role = domain.RoleSection
			if level == 1 && strings.EqualFold(block.Text, title) {
				role = domain.RoleBook
			}
		}
		node := &domain.OutlineNode{
			ID: stableID(parserVersion, "heading", block.ID), Title: block.Text, Role: role,
			Depth: level, StartBlock: block.Seq, EndBlockExclusive: uint64(len(blocks)),
			SourceTarget: block.Source, Confidence: domain.ConfidenceHigh,
			Provenance: domain.StructureHeading,
		}
		stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, node)
		stack = append(stack, node)
	}
	structure.ClassifyMajorUnits(root, blocks)
	return []*domain.OutlineNode{root}
}

func findElement(root *htmlnode.Node, tag string) *htmlnode.Node {
	var result *htmlnode.Node
	var walk func(*htmlnode.Node)
	walk = func(node *htmlnode.Node) {
		if result != nil || node == nil {
			return
		}
		if node.Type == htmlnode.ElementNode {
			if isHidden(node) || ignoredElement(node) {
				return
			}
			if node.Data == tag {
				result = node
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return result
}

func hasNarratableBlock(blocks []domain.Block) bool {
	for _, block := range blocks {
		if !block.SkipByDefault && block.Kind != domain.BlockBreak && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

func firstHeading(blocks []domain.Block) string {
	for _, block := range blocks {
		if block.Kind == domain.BlockHeading && strings.TrimSpace(block.Text) != "" {
			return block.Text
		}
	}
	return ""
}

func fallbackTitle(path string) string {
	title := strings.TrimSpace(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
	if title == "" || title == "." {
		return "Untitled document"
	}
	return title
}

func isHeading(node *htmlnode.Node) bool {
	return len(node.Data) == 2 && node.Data[0] == 'h' && node.Data[1] >= '1' && node.Data[1] <= '6'
}

func headingLevel(node *htmlnode.Node) int { return int(node.Data[1] - '0') }

func ignoredElement(node *htmlnode.Node) bool {
	switch node.Data {
	case "head", "script", "style", "template", "title", "noscript", "iframe", "svg":
		return true
	default:
		return false
	}
}

func isHidden(node *htmlnode.Node) bool {
	if hasAttribute(node, "hidden") || strings.EqualFold(attribute(node, "aria-hidden"), "true") {
		return true
	}
	style := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(attribute(node, "style"), " ", ""), "\n", ""))
	return strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden")
}

func isChrome(node, selectedRoot *htmlnode.Node, inContentContainer bool) bool {
	if isSuppressedTag(node) {
		return true
	}
	if node.Data == "header" && selectedRoot.Data == "body" && !inContentContainer {
		return true
	}
	for _, role := range strings.Fields(strings.ToLower(attribute(node, "role"))) {
		switch role {
		case "banner", "navigation", "complementary", "contentinfo", "search", "menu":
			return true
		}
	}
	return false
}

func isSuppressedTag(node *htmlnode.Node) bool {
	switch node.Data {
	case "nav", "footer", "aside":
		return true
	default:
		return false
	}
}

func isContentContainer(node *htmlnode.Node) bool {
	switch node.Data {
	case "article", "section", "main":
		return true
	default:
		return false
	}
}

func isSidebarLike(node *htmlnode.Node) bool {
	values := strings.Fields(strings.ToLower(attribute(node, "class") + " " + attribute(node, "id")))
	for _, value := range values {
		for _, marker := range []string{"sidebar", "side-bar", "related", "recommend", "advert", "promo", "cookie", "newsletter", "breadcrumb", "social-share", "share-tools", "site-header", "site-footer", "page-header", "page-footer"} {
			if strings.Contains(value, marker) {
				return true
			}
		}
	}
	return false
}

func isDecorative(node *htmlnode.Node) bool {
	return strings.EqualFold(attribute(node, "role"), "presentation") || strings.EqualFold(attribute(node, "aria-hidden"), "true")
}

func isNoteref(node *htmlnode.Node) bool {
	if strings.EqualFold(attribute(node, "role"), "doc-noteref") {
		return true
	}
	for _, semantic := range strings.Fields(strings.ToLower(attribute(node, "epub:type"))) {
		if semantic == "noteref" || semantic == "doc-noteref" {
			return true
		}
	}
	return false
}

func elementSemantics(node *htmlnode.Node) []string {
	var semantics []string
	switch node.Data {
	case "main", "article", "section":
		semantics = append(semantics, node.Data)
	case "ul":
		semantics = append(semantics, "unordered_list")
	case "ol":
		semantics = append(semantics, "ordered_list")
	}
	for _, name := range []string{"role", "epub:type"} {
		for _, token := range strings.Fields(strings.ToLower(attribute(node, name))) {
			semantics = append(semantics, token)
			if prefix, local, ok := strings.Cut(token, ":"); ok && prefix != "" && local != "" {
				semantics = append(semantics, local)
			}
		}
	}
	return uniqueStrings(semantics)
}

func codeLanguage(node *htmlnode.Node) []string {
	for _, className := range strings.Fields(attribute(node, "class")) {
		className = strings.TrimSpace(className)
		if strings.HasPrefix(strings.ToLower(className), "language-") && len(className) > len("language-") {
			return []string{"language:" + strings.TrimPrefix(className, "language-")}
		}
	}
	return nil
}

func attribute(node *htmlnode.Node, name string) string {
	name = strings.ToLower(name)
	for _, attr := range node.Attr {
		if strings.ToLower(attr.Key) == name {
			return attr.Val
		}
	}
	return ""
}

func hasAttribute(node *htmlnode.Node, name string) bool {
	name = strings.ToLower(name)
	for _, attr := range node.Attr {
		if strings.ToLower(attr.Key) == name {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func uniqueAuthors(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func domPath(node *htmlnode.Node) string {
	var reversed []string
	for current := node; current != nil && current.Type != htmlnode.DocumentNode; current = current.Parent {
		if current.Type != htmlnode.ElementNode {
			continue
		}
		index := 1
		for sibling := current.PrevSibling; sibling != nil; sibling = sibling.PrevSibling {
			if sibling.Type == htmlnode.ElementNode && sibling.Data == current.Data {
				index++
			}
		}
		reversed = append(reversed, fmt.Sprintf("%s[%d]", current.Data, index))
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return strings.Join(reversed, "/")
}

func stableID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(hash, part)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}
