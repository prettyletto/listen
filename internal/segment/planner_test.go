package segment

import (
	"strings"
	"testing"

	"github.com/prettyletto/listen/internal/domain"
)

func TestPlanDocumentSplitsOnSentenceAndWordLimits(t *testing.T) {
	doc := &domain.Document{
		ID:       "document",
		Metadata: domain.Metadata{Language: "en"},
		Blocks: []domain.Block{{
			ID: "block-a", Seq: 0, Kind: domain.BlockParagraph,
			Text:   "One thought. Another thought. A final thought that is longer.",
			Source: domain.SourceRef{Path: "/book.txt"},
		}},
		Outline: []*domain.OutlineNode{{
			ID: "book", Title: "Book", Role: domain.RoleBook,
			StartBlock: 0, EndBlockExclusive: 1,
		}},
	}
	plan, err := PlanDocument(doc, Options{TargetWords: 4, MaxWords: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) < 2 {
		t.Fatalf("long paragraph was not split: %#v", plan.Segments)
	}
	for i, item := range plan.Segments {
		if item.Seq != uint64(i) || item.ID == "" || item.SourceText != item.SpokenText || item.SourceLang != "en" {
			t.Fatalf("invalid segment identity or sequence: %#v", item)
		}
		if len(words(item.SourceText)) > 5 {
			t.Fatalf("segment exceeds hard word limit: %q", item.SourceText)
		}
	}
	if plan.Segments[0].SourceText != "One thought. Another thought." {
		t.Fatalf("sentence boundary was not preserved: %q", plan.Segments[0].SourceText)
	}
}

func TestPlanDocumentNeverCrossesMajorUnitBoundary(t *testing.T) {
	doc := &domain.Document{
		ID: "document",
		Blocks: []domain.Block{
			{ID: "a", Seq: 0, Kind: domain.BlockParagraph, Text: "First unit prose."},
			{ID: "b", Seq: 1, Kind: domain.BlockParagraph, Text: "Second unit prose."},
		},
		Outline: []*domain.OutlineNode{
			{ID: "chapter-a", Title: "Chapter A", Role: domain.RoleChapter, StartBlock: 0, EndBlockExclusive: 1},
			{ID: "chapter-b", Title: "Chapter B", Role: domain.RoleChapter, StartBlock: 1, EndBlockExclusive: 2},
		},
	}
	plan, err := PlanDocument(doc, Options{TargetWords: 100, MaxWords: 200})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("got %d segments, want one per chapter: %#v", len(plan.Segments), plan.Segments)
	}
	if plan.Segments[0].MajorUnitID == plan.Segments[1].MajorUnitID {
		t.Fatal("segments from separate chapters share a major unit")
	}
}

func TestPlanDocumentSkipsExcludedBlocks(t *testing.T) {
	doc := &domain.Document{
		ID: "document",
		Blocks: []domain.Block{
			{ID: "a", Seq: 0, Kind: domain.BlockParagraph, Text: "Read this."},
			{ID: "b", Seq: 1, Kind: domain.BlockCode, Text: "skip this."},
			{ID: "c", Seq: 2, Kind: domain.BlockParagraph, Text: "Skip by default.", SkipByDefault: true},
		},
	}
	plan, err := PlanDocument(doc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Segments) != 1 || plan.Segments[0].SourceText != "Read this." {
		t.Fatalf("excluded blocks were narrated: %#v", plan.Segments)
	}
}

func TestSegmentIDSurvivesUnrelatedEarlierInsertion(t *testing.T) {
	makeDocument := func(blocks []domain.Block) *domain.Document {
		return &domain.Document{ID: "content-hash-can-change", Blocks: blocks, Outline: []*domain.OutlineNode{{
			ID: "book", Title: "Book", Role: domain.RoleBook, StartBlock: 0, EndBlockExclusive: uint64(len(blocks)),
		}}}
	}
	original := domain.Block{ID: "stable-block", Seq: 0, Kind: domain.BlockParagraph, Text: "Keep my audio stable.", Source: domain.SourceRef{Path: "/book.txt"}}
	first, err := PlanDocument(makeDocument([]domain.Block{original}), Options{TargetWords: 1})
	if err != nil {
		t.Fatal(err)
	}
	inserted := domain.Block{ID: "new-block", Seq: 0, Kind: domain.BlockParagraph, Text: "New opening."}
	original.Seq = 1
	second, err := PlanDocument(makeDocument([]domain.Block{inserted, original}), Options{TargetWords: 1})
	if err != nil {
		t.Fatal(err)
	}
	if first.Segments[0].ID != second.Segments[1].ID {
		t.Fatalf("unrelated insertion changed stable ID: %s != %s", first.Segments[0].ID, second.Segments[1].ID)
	}
}

func words(text string) []string { return strings.Fields(text) }
