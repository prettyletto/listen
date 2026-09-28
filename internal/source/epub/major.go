package epub

import (
	"github.com/prettyletto/listen/internal/domain"
	"github.com/prettyletto/listen/internal/structure"
)

// Keep the format-local name so EPUB-specific tests read naturally; the rules
// themselves are shared with every source parser and the segment planner.
func classifyMajorUnits(root *domain.OutlineNode, blocks []domain.Block) {
	structure.ClassifyMajorUnits(root, blocks)
}
