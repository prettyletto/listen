package segment

import "github.com/prettyletto/listen/internal/domain"

type Kind string

const (
	KindProse   Kind = "prose"
	KindHeading Kind = "heading"
	KindCode    Kind = "code"
)

// Segment is the stable unit of translation, caching, ordering and playback.
// It is semantic: never split across chapter boundaries and prefer complete
// paragraphs/sentences over arbitrary character offsets.
type Segment struct {
	ID           string
	Seq          uint64
	Kind         Kind
	SourceText   string
	SpokenText   string
	SourceLang   string
	TargetLang   string
	SectionPath  []string
	MajorUnitID  string
	SourceBlocks []uint64
	Source       domain.SourceRef
}

type Plan struct {
	DocumentID string
	Segments   []Segment
}
