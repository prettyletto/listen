package structure

import (
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/domain"
)

func TestMajorUnitSubstantialThresholdUsesNarratableProse(t *testing.T) {
	unit := &domain.OutlineNode{Title: "A unit", StartBlock: 0, EndBlockExclusive: 1}
	if IsSubstantial(unit, []domain.Block{{Kind: domain.BlockParagraph, Text: strings.Repeat("word ", 79)}}) {
		t.Fatal("79 prose words should not meet the threshold")
	}
	if !IsSubstantial(unit, []domain.Block{{Kind: domain.BlockParagraph, Text: strings.Repeat("word ", 80)}}) {
		t.Fatal("80 prose words should meet the threshold")
	}
	threeBlocks := []domain.Block{
		{Kind: domain.BlockParagraph, Text: "first"},
		{Kind: domain.BlockQuote, Text: "second"},
		{Kind: domain.BlockListItem, Text: "third"},
	}
	unit.EndBlockExclusive = uint64(len(threeBlocks))
	if !IsSubstantial(unit, threeBlocks) {
		t.Fatal("three prose blocks should meet the threshold")
	}
	if IsSubstantial(unit, []domain.Block{{Kind: domain.BlockCode, Text: strings.Repeat("word ", 100)}}) {
		t.Fatal("code should not make a unit substantial")
	}
}

func TestClassifyRolesUsesSharedSemanticAndTitleEvidence(t *testing.T) {
	if got := ClassifyTitle("Chapter 4 — A New Beginning"); got != domain.RoleChapter {
		t.Fatalf("chapter title role = %q", got)
	}
	if got := ClassifySemantics([]string{"doc-part"}); got != domain.RolePart {
		t.Fatalf("part semantic role = %q", got)
	}
	if got := ClassifyBlock(domain.Block{Text: "Preface", Semantics: []string{"chapter"}}); got != domain.RoleChapter {
		t.Fatalf("explicit semantics did not take priority: %q", got)
	}
}
