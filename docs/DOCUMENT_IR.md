# Canonical Document IR

Correct book parsing depends on separating **reading order** from **navigation hierarchy**.

## Two dimensions

A book has:

1. A linear sequence of content that should be read.
2. A tree users navigate as parts, chapters, sections, subsections, appendices, etc.

Do not encode the tree by physically nesting text. Store one canonical linear block list, then map outline nodes to intervals over it.

## Block invariants

`Document.Blocks` is canonical reading order.

For every block:

- `Seq` is contiguous starting at zero.
- `Text` is source content, not translated content.
- `SourceRef` points back to enough source information for diagnostics.
- `SkipByDefault` can suppress navigation pages, code, page numbers, footnotes, etc. without deleting provenance.

## Outline invariants

Every outline node uses `[StartBlock, EndBlockExclusive)`.

For a valid tree:

- `StartBlock < EndBlockExclusive` unless the node is an intentional alias.
- child start/end are contained by parent range;
- sibling ranges are monotonically ordered;
- overlapping siblings are invalid except explicitly marked navigation aliases in a future schema;
- the outline may omit uninteresting blocks, but reading order may not.

Navigation entries that target resources outside a format's linear reading
order cannot receive a valid block interval. Preserve those entries separately
as auxiliary navigation metadata, including their source target and parent
labels. They do not contribute blocks to default playback.

## Structure confidence

Each node gets `high`, `medium`, or `low` confidence based on source authority.

Examples:

- EPUB nav/NCX target successfully mapped: high.
- Markdown heading AST: high.
- PDF bookmark mapped to page: high/medium depending on text mapping.
- plain-text explicit `CHAPTER 4`: high.
- isolated all-caps line: medium; repeated title-case heading patterns: low.

Markdown front matter, fenced/indented code, plain-text probable contents
entries, and Gutenberg boilerplate remain as blocks with `SkipByDefault` set.
They retain source positions while staying out of the default speech plan.

Confidence is primarily diagnostic. Default UX does not stop to ask the user.

## Roles

Roles are semantic and independent of depth:

- book
- part
- chapter
- section
- front_matter
- back_matter
- appendix
- unknown

A top-level `Preface` can be front matter; a depth-2 `Chapter 1` is still a chapter.

## Major unit

The segment planner computes a `MajorUnitID`, normally chapter/appendix/front-matter unit. It is important for:

- chapter navigation;
- export tracks;
- translator consistency boundaries;
- fallback provider switching;
- session resume labels.

Do not equate “major unit” with outline depth 1. Many EPUBs wrap chapters inside Parts or a single book container.
