package domain

// Format is the detected source representation.
type Format string

const (
	FormatText      Format = "text"
	FormatMarkdown  Format = "markdown"
	FormatHTML      Format = "html"
	FormatEPUB      Format = "epub"
	FormatPDF       Format = "pdf"
	FormatCode      Format = "code"
	FormatDirectory Format = "directory"
)

type Metadata struct {
	Title    string
	Authors  []string
	Language string
}

// SourceRef is stable source provenance. Parsers should populate the fields
// relevant to their format and leave the others empty.
type SourceRef struct {
	Path       string
	Fragment   string
	ByteStart  int64
	ByteEnd    int64
	Page       int
	DOMPath    string
	SpineIndex int // Zero-based for EPUB spine items; -1 for a known resource outside the spine.
}

type BlockKind string

const (
	BlockHeading   BlockKind = "heading"
	BlockParagraph BlockKind = "paragraph"
	BlockListItem  BlockKind = "list_item"
	BlockQuote     BlockKind = "quote"
	BlockCode      BlockKind = "code"
	BlockTableRow  BlockKind = "table_row"
	BlockNote      BlockKind = "note"
	BlockBreak     BlockKind = "break"
)

type Block struct {
	ID            string
	Seq           uint64
	Kind          BlockKind
	Text          string
	Level         int
	Source        SourceRef
	Semantics     []string
	SkipByDefault bool
}

type OutlineRole string

const (
	RoleUnknown     OutlineRole = "unknown"
	RoleBook        OutlineRole = "book"
	RolePart        OutlineRole = "part"
	RoleChapter     OutlineRole = "chapter"
	RoleSection     OutlineRole = "section"
	RoleFrontMatter OutlineRole = "front_matter"
	RoleBackMatter  OutlineRole = "back_matter"
	RoleAppendix    OutlineRole = "appendix"
)

type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

type StructureSource string

const (
	StructureEPUBNav     StructureSource = "epub_nav"
	StructureNCX         StructureSource = "ncx"
	StructureHeading     StructureSource = "content_heading"
	StructurePDFBookmark StructureSource = "pdf_bookmark"
	StructureHeuristic   StructureSource = "heuristic"
	StructureDirectory   StructureSource = "directory"
	StructureCodeAST     StructureSource = "code_ast"
)

// OutlineNode maps structure to a half-open interval over Document.Blocks.
// Children must be contained by their parent range.
type OutlineNode struct {
	ID                string
	Title             string
	Role              OutlineRole
	Depth             int
	StartBlock        uint64
	EndBlockExclusive uint64
	SourceTarget      SourceRef
	Confidence        Confidence
	Provenance        StructureSource
	Children          []*OutlineNode
}

// AuxiliaryNavigationTarget preserves a navigation entry that points outside
// the linear reading spine. It is available for future explicit selection but
// never contributes blocks to the default reading stream.
type AuxiliaryNavigationTarget struct {
	ID             string
	Title          string
	NavigationPath []string
	SourceTarget   SourceRef
	Provenance     StructureSource
}

type Document struct {
	ID                  string
	Format              Format
	Metadata            Metadata
	Blocks              []Block
	Outline             []*OutlineNode
	AuxiliaryNavigation []AuxiliaryNavigationTarget
	Warnings            []string
}
