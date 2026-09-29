package pdf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/structure"
)

type qpdfOutline struct {
	Title           string        `json:"title"`
	DestinationPage *int          `json:"destpageposfrom1"`
	Children        []qpdfOutline `json:"kids"`
}

type qpdfOutlineResult struct {
	Outlines []qpdfOutline `json:"outlines"`
}

func readBookmarks(ctx context.Context, run commandRunner, path string) ([]qpdfOutline, string, error) {
	if _, err := exec.LookPath("qpdf"); err != nil {
		return nil, "PDF bookmarks were not available; visual structure was inferred from page layout.", nil
	}
	data, err := run(ctx, "qpdf", "--json", "--json-key=outlines", path)
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrResourceLimit) {
			return nil, "", err
		}
		return nil, "PDF bookmarks could not be read; visual structure was inferred from page layout.", nil
	}
	var result qpdfOutlineResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "PDF bookmarks could not be read; visual structure was inferred from page layout.", nil
	}
	if err := validateBookmarkLimits(result.Outlines); err != nil {
		return nil, "", err
	}
	if len(result.Outlines) == 0 {
		return nil, "This PDF has no bookmarks; visual structure was inferred from page layout.", nil
	}
	return result.Outlines, "", nil
}

func validateBookmarkLimits(nodes []qpdfOutline) error {
	count := 0
	var visit func([]qpdfOutline, int) error
	visit = func(items []qpdfOutline, depth int) error {
		if depth > maxPDFBookmarkDepth {
			return fmt.Errorf("%w: PDF bookmark nesting exceeds %d levels", ErrResourceLimit, maxPDFBookmarkDepth)
		}
		for _, item := range items {
			count++
			if count > maxPDFBookmarks {
				return fmt.Errorf("%w: PDF contains more than %d bookmarks", ErrResourceLimit, maxPDFBookmarks)
			}
			if err := visit(item.Children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(nodes, 1)
}

func outlineFromBookmarks(bookmarks []qpdfOutline, blocks []domain.Block, path, documentID string) []*domain.OutlineNode {
	return bookmarkNodes(bookmarks, blocks, path, documentID, 0, uint64(len(blocks)), 1, "")
}

func bookmarkNodes(bookmarks []qpdfOutline, blocks []domain.Block, path, documentID string, parentStart, parentEnd uint64, depth int, parentID string) []*domain.OutlineNode {
	type candidate struct {
		bookmark qpdfOutline
		start    uint64
		page     int
		ordinal  int
	}
	items := make([]candidate, 0, len(bookmarks))
	for ordinal, bookmark := range bookmarks {
		if bookmark.DestinationPage == nil || *bookmark.DestinationPage <= 0 || strings.TrimSpace(bookmark.Title) == "" {
			continue
		}
		start, ok := firstBlockOnOrAfterPage(blocks, *bookmark.DestinationPage)
		if !ok || start < parentStart || start >= parentEnd {
			continue
		}
		items = append(items, candidate{bookmark: bookmark, start: start, page: *bookmark.DestinationPage, ordinal: ordinal})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].start != items[j].start {
			return items[i].start < items[j].start
		}
		return items[i].ordinal < items[j].ordinal
	})
	result := make([]*domain.OutlineNode, 0, len(items))
	for i, item := range items {
		end := parentEnd
		for _, next := range items[i+1:] {
			if next.start > item.start {
				end = next.start
				break
			}
		}
		if end <= item.start {
			end = item.start + 1
		}
		if end > parentEnd {
			end = parentEnd
		}
		id := stableID(parserVersion, documentID, parentID, item.bookmark.Title, fmt.Sprint(item.page), fmt.Sprint(depth), fmt.Sprint(item.ordinal))
		node := &domain.OutlineNode{
			ID: id, Title: strings.TrimSpace(item.bookmark.Title), Role: structure.ClassifyTitle(item.bookmark.Title),
			Depth: depth, StartBlock: item.start, EndBlockExclusive: end,
			SourceTarget: domain.SourceRef{Path: path, Page: item.page},
			Confidence:   domain.ConfidenceHigh, Provenance: domain.StructurePDFBookmark,
		}
		node.Children = bookmarkNodes(item.bookmark.Children, blocks, path, documentID, item.start, end, depth+1, id)
		result = append(result, node)
	}
	return result
}

func firstBlockOnOrAfterPage(blocks []domain.Block, page int) (uint64, bool) {
	index := sort.Search(len(blocks), func(i int) bool { return blocks[i].Source.Page >= page })
	if index >= len(blocks) {
		return 0, false
	}
	return uint64(index), true
}

func outlineFromHeadings(blocks []domain.Block, path, documentID string) []*domain.OutlineNode {
	type stackItem struct {
		node  *domain.OutlineNode
		depth int
	}
	var roots []*domain.OutlineNode
	var stack []stackItem
	for index, block := range blocks {
		if block.Kind != domain.BlockHeading {
			continue
		}
		depth := block.Level
		if depth < 1 {
			depth = 1
		}
		if depth > 6 {
			depth = 6
		}
		for len(stack) > 0 && stack[len(stack)-1].depth >= depth {
			stack[len(stack)-1].node.EndBlockExclusive = uint64(index)
			stack = stack[:len(stack)-1]
		}
		id := stableID(parserVersion, documentID, block.ID, fmt.Sprint(depth))
		node := &domain.OutlineNode{
			ID: id, Title: block.Text, Role: structure.ClassifyTitle(block.Text), Depth: depth,
			StartBlock: uint64(index), EndBlockExclusive: uint64(len(blocks)),
			SourceTarget: block.Source, Confidence: domain.ConfidenceLow, Provenance: domain.StructureHeuristic,
		}
		if len(stack) == 0 {
			roots = append(roots, node)
		} else {
			stack[len(stack)-1].node.Children = append(stack[len(stack)-1].node.Children, node)
		}
		stack = append(stack, stackItem{node: node, depth: depth})
	}
	return roots
}
