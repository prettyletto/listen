// Package structure contains format-independent document structure rules.
package structure

import (
	"strings"
	"unicode"

	"github.com/prettyletto/listen/internal/domain"
)

// ClassifyTitle assigns a role only when a title uses a recognizable structural
// label. Layout-sensitive formats should require their own additional evidence.
func ClassifyTitle(title string) domain.OutlineRole {
	title = strings.ToLower(strings.TrimSpace(title))
	for _, prefix := range []string{"part ", "parte ", "book ", "livro ", "volume ", "vol. "} {
		if strings.HasPrefix(title, prefix) {
			return domain.RolePart
		}
	}
	for _, prefix := range []string{"chapter ", "capítulo ", "capitulo ", "chapitre ", "capitolo ", "capítulo\t"} {
		if strings.HasPrefix(title, prefix) {
			return domain.RoleChapter
		}
	}
	for _, prefix := range []string{"section ", "seção ", "secao ", "sección ", "seccion ", "section\t"} {
		if strings.HasPrefix(title, prefix) {
			return domain.RoleSection
		}
	}
	for _, prefix := range []string{"appendix", "apêndice", "apendice", "annexe", "anexo"} {
		if strings.HasPrefix(title, prefix) {
			return domain.RoleAppendix
		}
	}
	for _, prefix := range []string{"preface", "foreword", "prologue", "prólogo", "prologo", "introduction", "introdução", "introducao", "dedication", "dedicatória", "acknowledg", "copyright", "title page"} {
		if strings.HasPrefix(title, prefix) {
			return domain.RoleFrontMatter
		}
	}
	for _, prefix := range []string{"epilogue", "epílogo", "epilogo", "conclusion", "postface", "bibliography", "bibliografia", "references", "glossary", "glossário", "glossario", "index", "índice", "indice", "endnotes"} {
		if strings.HasPrefix(title, prefix) {
			return domain.RoleBackMatter
		}
	}
	return domain.RoleUnknown
}

// ClassifySemantics maps common EPUB/HTML semantic tokens to shared roles.
func ClassifySemantics(semantics []string) domain.OutlineRole {
	for _, semantic := range semantics {
		switch strings.TrimPrefix(strings.ToLower(semantic), "doc-") {
		case "part":
			return domain.RolePart
		case "chapter":
			return domain.RoleChapter
		case "appendix":
			return domain.RoleAppendix
		case "frontmatter", "front-matter", "preface", "foreword", "prologue", "introduction", "dedication", "acknowledgments", "copyright-page", "titlepage":
			return domain.RoleFrontMatter
		case "backmatter", "back-matter", "epilogue", "conclusion", "bibliography", "references", "glossary", "index", "endnotes":
			return domain.RoleBackMatter
		}
	}
	return domain.RoleUnknown
}

// ClassifyBlock gives explicit format semantics priority over a visible title.
func ClassifyBlock(block domain.Block) domain.OutlineRole {
	if role := ClassifySemantics(block.Semantics); role != domain.RoleUnknown {
		return role
	}
	return ClassifyTitle(block.Text)
}

// IsMajorRole reports whether a role defines a playback/export boundary.
func IsMajorRole(role domain.OutlineRole) bool {
	switch role {
	case domain.RolePart, domain.RoleChapter, domain.RoleFrontMatter, domain.RoleBackMatter, domain.RoleAppendix:
		return true
	default:
		return false
	}
}

// ClassifyMajorUnits promotes structural children only when their content is
// substantial. It never changes block order or outline ranges.
func ClassifyMajorUnits(root *domain.OutlineNode, blocks []domain.Block) {
	if root == nil || hasChapterRole(root) {
		return
	}
	var promote func(*domain.OutlineNode)
	promotedPartChildren := false
	promote = func(node *domain.OutlineNode) {
		if node == nil {
			return
		}
		if node.Role == domain.RolePart {
			var substantial []*domain.OutlineNode
			for _, child := range node.Children {
				if IsSubstantial(child, blocks) {
					substantial = append(substantial, child)
				}
			}
			if len(substantial) >= 2 {
				for _, child := range substantial {
					if child.Role == domain.RoleUnknown || child.Role == domain.RoleSection {
						child.Role = domain.RoleChapter
					}
				}
				promotedPartChildren = true
			}
		}
		for _, child := range node.Children {
			promote(child)
		}
	}
	promote(root)
	if promotedPartChildren {
		return
	}

	parents := []*domain.OutlineNode{root}
	for len(parents) > 0 {
		var next []*domain.OutlineNode
		var promotedAtDepth bool
		for _, parent := range parents {
			var substantial []*domain.OutlineNode
			for _, child := range parent.Children {
				if child.Role != domain.RoleChapter && child.Role != domain.RoleFrontMatter && child.Role != domain.RoleBackMatter && child.Role != domain.RoleAppendix && IsSubstantial(child, blocks) {
					substantial = append(substantial, child)
				}
				next = append(next, child)
			}
			if len(substantial) >= 2 {
				for _, child := range substantial {
					if child.Role == domain.RoleUnknown || child.Role == domain.RoleSection {
						child.Role = domain.RoleChapter
					}
				}
				promotedAtDepth = true
			}
		}
		if promotedAtDepth {
			return
		}
		parents = next
	}
}

// IsSubstantial uses narratable prose volume, excluding headings, notes,
// skipped blocks, and code. Recognized front/back matter may remain short
// because its role comes from stronger semantic or title evidence.
func IsSubstantial(node *domain.OutlineNode, blocks []domain.Block) bool {
	if node == nil || isNonNarrativeUnitTitle(node.Title) {
		return false
	}
	start, end := node.StartBlock, node.EndBlockExclusive
	if end > uint64(len(blocks)) {
		end = uint64(len(blocks))
	}
	if start > end {
		return false
	}
	var wordCount, blockCount int
	for seq := start; seq < end; seq++ {
		block := blocks[seq]
		if block.SkipByDefault || block.Kind == domain.BlockBreak || block.Kind == domain.BlockNote || block.Kind == domain.BlockCode || block.Kind == domain.BlockHeading {
			continue
		}
		words := strings.Fields(block.Text)
		if len(words) == 0 {
			continue
		}
		wordCount += len(words)
		blockCount++
	}
	return wordCount >= 80 || blockCount >= 3
}

func hasChapterRole(node *domain.OutlineNode) bool {
	if node == nil {
		return false
	}
	if node.Role == domain.RoleChapter {
		return true
	}
	for _, child := range node.Children {
		if hasChapterRole(child) {
			return true
		}
	}
	return false
}

func isNonNarrativeUnitTitle(title string) bool {
	title = strings.ToLower(strings.TrimSpace(title))
	for _, prefix := range []string{"contents", "table of contents", "cover", "copyright", "title page", "colophon"} {
		if title == prefix || strings.HasPrefix(title, prefix+" ") {
			return true
		}
	}
	first, _, _ := strings.Cut(title, " ")
	return first != "" && strings.TrimFunc(first, unicode.IsPunct) == "toc"
}
