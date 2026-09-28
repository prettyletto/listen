package epub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/structure"
)

type mappedNavigationNode struct {
	entry    *rawNavigationNode
	start    uint64
	source   domain.SourceRef
	children []*mappedNavigationNode
}

func buildOutline(ctx context.Context, pkg packageDocument, navPath string, nav []*rawNavigationNode, provenance domain.StructureSource, files map[string]*contentFile, blocks []domain.Block, warnings *[]string) []*domain.OutlineNode {
	root := &domain.OutlineNode{
		ID: epubID("book", pkg.identifier, pkg.title, pkg.path), Title: pkg.title,
		Role: domain.RoleBook, Depth: 0, StartBlock: 0,
		EndBlockExclusive: uint64(len(blocks)), Confidence: domain.ConfidenceHigh,
		Provenance: provenance, SourceTarget: domain.SourceRef{Path: pkg.path},
	}
	if root.Title == "" {
		root.Title = "Untitled EPUB"
	}
	if len(blocks) == 0 {
		return []*domain.OutlineNode{root}
	}
	var mapped []*mappedNavigationNode
	if len(nav) > 0 {
		mapped = mapNavigation(ctx, navPath, nav, pkg, files, blocks, warnings)
	}
	if len(mapped) == 0 {
		if provenance != "" {
			addWarning(warnings, "EPUB navigation targets could not be mapped; XHTML headings and spine document boundaries were used.")
		}
		root.Provenance = domain.StructureHeading
		root.Confidence = domain.ConfidenceMedium
		root.Children = buildHeadingsInRange(blocks, 0, uint64(len(blocks)), root.Depth)
		if len(root.Children) == 0 {
			root.Children = buildSpineRanges(pkg, files, uint64(len(blocks)), root.Depth)
		}
		classifyMajorUnits(root, blocks)
		return []*domain.OutlineNode{root}
	}
	root.Children = mapNavigationRanges(mapped, 0, uint64(len(blocks)), 1, provenance, warnings)
	enrichOutlineWithHeadings(root, blocks)
	classifyMajorUnits(root, blocks)
	return []*domain.OutlineNode{root}
}

func mapNavigation(ctx context.Context, base string, entries []*rawNavigationNode, pkg packageDocument, files map[string]*contentFile, blocks []domain.Block, warnings *[]string) []*mappedNavigationNode {
	var mapOne func(*rawNavigationNode) *mappedNavigationNode
	mapOne = func(entry *rawNavigationNode) *mappedNavigationNode {
		if err := ctx.Err(); err != nil {
			return nil
		}
		node := &mappedNavigationNode{entry: entry}
		for _, child := range entry.children {
			if mapped := mapOne(child); mapped != nil {
				node.children = append(node.children, mapped)
			}
		}
		var ownStart uint64
		ownValid := false
		if entry.href != "" {
			resourcePath, fragment, err := resolveArchiveReference(base, entry.href)
			if err != nil {
				addWarning(warnings, fmt.Sprintf("EPUB navigation target %q could not be resolved: %v.", entry.href, err))
			} else if file, exists := files[resourcePath]; !exists {
				switch {
				case !hasManifestResource(pkg, resourcePath):
					addWarning(warnings, fmt.Sprintf("EPUB navigation entry %q references %q, which is absent from the package manifest and was omitted.", entry.title, entry.href))
				case hasLinearSpineResource(pkg, resourcePath):
					addWarning(warnings, fmt.Sprintf("EPUB navigation entry %q targets %q, a linear spine resource with no readable blocks.", entry.title, entry.href))
				default:
					addWarning(warnings, fmt.Sprintf("EPUB navigation entry %q targets %q outside the linear reading spine; it is retained as auxiliary navigation metadata.", entry.title, entry.href))
				}
			} else {
				ownValid = true
				ownStart = file.start
				if fragment != "" {
					if exact, found := file.targets[fragment]; found {
						ownStart = exact
					} else {
						addWarning(warnings, fmt.Sprintf("EPUB navigation fragment %q was missing; mapped %q to the start of its spine document.", fragment, resourcePath))
					}
				}
				node.source = domain.SourceRef{Path: resourcePath, Fragment: fragment, SpineIndex: file.index}
				if ownStart >= uint64(len(blocks)) {
					ownValid = false
					addWarning(warnings, fmt.Sprintf("EPUB navigation target %q has no readable block.", entry.href))
				}
			}
		}
		if ownValid {
			node.start = ownStart
		} else if len(node.children) > 0 {
			node.start = node.children[0].start
			for _, child := range node.children[1:] {
				if child.start < node.start {
					node.start = child.start
				}
			}
		} else {
			return nil
		}
		for _, child := range node.children {
			if child.start < node.start {
				node.start = child.start
				addWarning(warnings, fmt.Sprintf("EPUB navigation parent %q was expanded to contain an earlier child target.", entry.title))
			}
		}
		return node
	}
	var result []*mappedNavigationNode
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			break
		}
		if node := mapOne(entry); node != nil {
			result = append(result, node)
		}
	}
	return result
}

func collectAuxiliaryNavigation(ctx context.Context, base string, entries []*rawNavigationNode, provenance domain.StructureSource, pkg packageDocument, files map[string]*contentFile) ([]domain.AuxiliaryNavigationTarget, error) {
	spineIndexes := make(map[string]int, len(pkg.spine))
	for _, item := range pkg.spine {
		if _, exists := spineIndexes[item.path]; !exists {
			spineIndexes[item.path] = item.index
		}
	}
	var result []domain.AuxiliaryNavigationTarget
	var visit func(*rawNavigationNode, []string) error
	visit = func(entry *rawNavigationNode, parents []string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		title := strings.TrimSpace(entry.title)
		if entry.href != "" {
			resourcePath, fragment, err := resolveArchiveReference(base, entry.href)
			if err == nil && !hasLinearSpineResource(pkg, resourcePath) && hasManifestResource(pkg, resourcePath) {
				if _, inLinearSpine := files[resourcePath]; !inLinearSpine {
					spineIndex, isSpineResource := spineIndexes[resourcePath]
					if !isSpineResource {
						spineIndex = -1
					}
					navigationPath := append([]string(nil), parents...)
					result = append(result, domain.AuxiliaryNavigationTarget{
						ID:    epubID("auxiliary-navigation", string(provenance), strings.Join(navigationPath, "\x00"), title, resourcePath, fragment),
						Title: title, NavigationPath: navigationPath,
						SourceTarget: domain.SourceRef{Path: resourcePath, Fragment: fragment, SpineIndex: spineIndex},
						Provenance:   provenance,
					})
				}
			}
		}
		nextParents := parents
		if title != "" {
			nextParents = append(append([]string(nil), parents...), title)
		}
		for _, child := range entry.children {
			if err := visit(child, nextParents); err != nil {
				return err
			}
		}
		return nil
	}
	for _, entry := range entries {
		if err := visit(entry, nil); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func hasManifestResource(pkg packageDocument, resourcePath string) bool {
	for _, item := range pkg.manifestList {
		if item.path == resourcePath {
			return true
		}
	}
	return false
}

func hasLinearSpineResource(pkg packageDocument, resourcePath string) bool {
	for _, item := range pkg.spine {
		if item.path == resourcePath && item.linear {
			return true
		}
	}
	return false
}

func mapNavigationRanges(nodes []*mappedNavigationNode, parentStart, parentEnd uint64, depth int, provenance domain.StructureSource, warnings *[]string) []*domain.OutlineNode {
	if len(nodes) == 0 {
		return nil
	}
	originalOrder := append([]*mappedNavigationNode(nil), nodes...)
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].start < nodes[j].start })
	for i := range nodes {
		if nodes[i] != originalOrder[i] {
			addWarning(warnings, "EPUB navigation entries were reordered to match spine reading order.")
			break
		}
	}
	var unique []*mappedNavigationNode
	for _, node := range nodes {
		if len(unique) > 0 && unique[len(unique)-1].start == node.start {
			addWarning(warnings, fmt.Sprintf("EPUB navigation label %q shares a start with a sibling; its children were merged with the earlier label.", node.entry.title))
			unique[len(unique)-1].children = append(unique[len(unique)-1].children, node.children...)
			continue
		}
		unique = append(unique, node)
	}
	nodes = unique
	result := make([]*domain.OutlineNode, 0, len(nodes))
	for index, node := range nodes {
		if node.start < parentStart || node.start >= parentEnd {
			addWarning(warnings, fmt.Sprintf("EPUB navigation entry %q falls outside its parent range and was omitted.", node.entry.title))
			continue
		}
		end := parentEnd
		if index+1 < len(nodes) {
			end = nodes[index+1].start
		}
		if end <= node.start {
			addWarning(warnings, fmt.Sprintf("EPUB navigation entry %q has an empty range and was omitted.", node.entry.title))
			continue
		}
		title := strings.TrimSpace(node.entry.title)
		if title == "" {
			title = path.Base(node.source.Path)
		}
		// Role enrichment from content is applied after authoritative ranges
		// have been built; navigation labels provide the initial classification.
		role := classifyTitle(title)
		outline := &domain.OutlineNode{
			ID:    epubID("navigation", node.source.Path, node.source.Fragment, title),
			Title: title, Role: role, Depth: depth,
			StartBlock: node.start, EndBlockExclusive: end,
			SourceTarget: node.source, Confidence: domain.ConfidenceHigh,
			Provenance: provenance,
		}
		outline.Children = mapNavigationRanges(node.children, node.start, end, depth+1, provenance, warnings)
		result = append(result, outline)
	}
	return result
}

func enrichOutlineWithHeadings(root *domain.OutlineNode, blocks []domain.Block) {
	var enrich func(*domain.OutlineNode)
	enrich = func(node *domain.OutlineNode) {
		for _, child := range node.Children {
			enrich(child)
		}
		if node.Role != domain.RoleBook && node.SourceTarget.Path != "" && node.StartBlock < uint64(len(blocks)) && node.Role == domain.RoleUnknown {
			if role := classifyBlock(blocks[node.StartBlock]); role != domain.RoleUnknown {
				node.Role = role
			}
		}
		cursor := node.StartBlock + 1
		for _, child := range node.Children {
			if cursor < child.StartBlock {
				childRange := buildHeadingsInRange(blocks, cursor, child.StartBlock, node.Depth)
				node.Children = append(node.Children, childRange...)
			}
			if child.EndBlockExclusive > cursor {
				cursor = child.EndBlockExclusive
			}
		}
		if cursor < node.EndBlockExclusive {
			node.Children = append(node.Children, buildHeadingsInRange(blocks, cursor, node.EndBlockExclusive, node.Depth)...)
		}
		sort.SliceStable(node.Children, func(i, j int) bool { return node.Children[i].StartBlock < node.Children[j].StartBlock })
	}
	enrich(root)
}

type headingOutline struct {
	block    domain.Block
	children []*headingOutline
}

func buildHeadingsInRange(blocks []domain.Block, start, end uint64, parentDepth int) []*domain.OutlineNode {
	if end > uint64(len(blocks)) {
		end = uint64(len(blocks))
	}
	if start >= end {
		return nil
	}
	var roots []*headingOutline
	var stack []*headingOutline
	for seq := start; seq < end; seq++ {
		block := blocks[seq]
		if block.Kind != domain.BlockHeading || block.SkipByDefault || strings.TrimSpace(block.Text) == "" {
			continue
		}
		level := block.Level
		if level <= 0 {
			level = 1
		}
		for len(stack) > 0 && stack[len(stack)-1].block.Level >= level {
			stack = stack[:len(stack)-1]
		}
		entry := &headingOutline{block: block}
		if len(stack) == 0 {
			roots = append(roots, entry)
		} else {
			stack[len(stack)-1].children = append(stack[len(stack)-1].children, entry)
		}
		stack = append(stack, entry)
	}
	return convertHeadingOutline(roots, end, parentDepth)
}

func convertHeadingOutline(entries []*headingOutline, rangeEnd uint64, parentDepth int) []*domain.OutlineNode {
	result := make([]*domain.OutlineNode, 0, len(entries))
	for index, entry := range entries {
		end := rangeEnd
		if index+1 < len(entries) {
			end = entries[index+1].block.Seq
		}
		block := entry.block
		title := strings.TrimSpace(block.Text)
		node := &domain.OutlineNode{
			ID:    epubID("heading", block.ID, title),
			Title: title, Role: classifyBlock(block), Depth: parentDepth + 1,
			StartBlock: block.Seq, EndBlockExclusive: end,
			SourceTarget: block.Source, Confidence: domain.ConfidenceHigh,
			Provenance: domain.StructureHeading,
		}
		node.Children = convertHeadingOutline(entry.children, end, node.Depth)
		result = append(result, node)
	}
	return result
}

func buildSpineRanges(pkg packageDocument, files map[string]*contentFile, blockCount uint64, parentDepth int) []*domain.OutlineNode {
	var result []*domain.OutlineNode
	for _, item := range pkg.spine {
		file, ok := files[item.path]
		if !ok || file.start >= file.end || file.start >= blockCount {
			continue
		}
		end := file.end
		if end > blockCount {
			end = blockCount
		}
		name := strings.TrimSuffix(path.Base(item.path), path.Ext(item.path))
		result = append(result, &domain.OutlineNode{
			ID: epubID("spine", item.path), Title: name,
			Role: domain.RoleChapter, Depth: parentDepth + 1,
			StartBlock: file.start, EndBlockExclusive: end,
			SourceTarget: domain.SourceRef{Path: item.path, SpineIndex: item.index},
			Confidence:   domain.ConfidenceMedium, Provenance: domain.StructureHeuristic,
		})
	}
	return result
}

func classifyBlock(block domain.Block) domain.OutlineRole {
	return structure.ClassifyBlock(block)
}

func classifyTitle(title string) domain.OutlineRole {
	return structure.ClassifyTitle(title)
}

func classifySemantics(semantics []string) domain.OutlineRole {
	return structure.ClassifySemantics(semantics)
}

func addWarning(warnings *[]string, message string) {
	for _, existing := range *warnings {
		if existing == message {
			return
		}
	}
	*warnings = append(*warnings, message)
}

func epubID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:16])
}
