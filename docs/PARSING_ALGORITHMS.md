# Parsing algorithms — implementation pseudocode

This file turns the parsing specification into concrete algorithms. Names are illustrative; keep equivalent behavior if implementation details differ.

## A. Parse pipeline

```text
DetectFormat(input)
  -> parser.Parse(input)
  -> NormalizeBlocks
  -> Build/MapOutline
  -> ValidateOutline
  -> ClassifyRoles
  -> SelectMajorUnits
  -> HashStructure
  -> Document
```

The parser may build intermediate data, but only the validated `Document` crosses the parser boundary.

## B. Outline range derivation

Input: tree nodes each with a resolved `start` block, plus total block count.

Recursive approach:

```text
assignRange(nodes, parentEnd):
    for i, node in nodes:
        nextSiblingStart = parentEnd
        if i+1 < len(nodes):
            nextSiblingStart = nodes[i+1].start

        node.end = nextSiblingStart
        if node.children:
            assignRange(node.children, node.end)

        # Parent start may equal first child start; allowed.
        # Child end must never exceed node.end.
```

This works when sibling starts are monotonic. If they are not:

1. attempt href/path normalization repairs;
2. if still non-monotonic, preserve spine order and drop/re-parent only the invalid navigation relationship;
3. add a warning containing source nav label/target;
4. never reorder body blocks to satisfy a broken TOC.

For a flat heading-derived outline, first construct a tree using a stack by normalized depth, then run the same range assignment.

## C. Mapping EPUB nav href -> block

Maintain indexes while extracting XHTML:

```text
DocumentStart[path] -> blockSeq
AnchorStart[path][elementID] -> blockSeq
```

When entering an element with an `id`, do not immediately bind it if no narratable block exists yet. Push it as a pending ancestor anchor. When the first descendant narratable block is emitted, bind all pending anchors to that block.

For an ID directly on a heading/paragraph, bind to that block.

Resolution:

```text
resolve(href, navPath):
    path, fragment = normalizeRelativeURL(navPath, href)
    path = canonicalEPUBPath(path)

    if fragment != "" and AnchorStart[path][fragment] exists:
        return blockSeq, exact

    if DocumentStart[path] exists:
        if fragment != "": warning(missing fragment)
        return DocumentStart[path], fallback-document-start

    return unresolved
```

Never fetch external URLs.

## D. EPUB block extraction state

DOM traversal keeps:

- current semantics inherited from ancestors;
- hidden state;
- pending anchor IDs;
- current section/heading path only for provenance, not outline authority;
- noteref state;
- note-body state;

Pseudo:

```text
walk(node):
    if hidden/script/style/template: return
    collect node.id into pendingAnchors
    update semantic stack from epub:type/role

    if node is a block producer:
        text = collectInline(node)
        if meaningful(text):
            seq = emitBlock(...)
            bind pendingAnchors -> seq
            bind node.id -> seq
        return  # do not double-emit descendants as paragraphs

    for child in node.children:
        walk(child)
```

Block producer traversal must specially handle nested list/table structures to avoid both parent and child text duplication.

## E. Enrich authoritative EPUB outline with XHTML headings

Given authoritative node ranges and heading blocks:

```text
for each heading block H in spine order:
    if H is the heading already represented by an authoritative node at H.seq:
        merge heading semantics into that node
        continue

    container = deepest outline node whose range strictly contains H.seq
    if container does not exist:
        continue

    # Build a local heading stack scoped to container.
    while stack top headingLevel >= H.headingLevel:
        pop
    parent = stack top if present else container
    create inferred section node(parent, start=H.seq, provenance=xhtml-heading)
    push it
```

After inferred starts are inserted, derive their ends **within the already-fixed authoritative parent range**. Inferred nodes can subdivide authoritative ranges but never extend them. Deduplicate by source start before title text because publisher wording in nav and visible heading can differ slightly.

## F. Major-unit classifier

Create candidates from outline nodes. Each node has:

- role evidence;
- depth;
- narratable words in its interval excluding skipped descendants where appropriate;
- child count;
- sibling pattern consistency.

Algorithm:

```text
if explicit chapter/appendix/front/back narrative roles exist:
    choose those in reading order
else for each Part:
    if >=2 substantial narrative children:
        choose children
if still <2 units:
    for depth from shallow to deep:
        candidates = substantial nodes at depth
        if len(candidates) >= 2 and coverage(candidates) >= threshold:
            choose candidates
            break
if no reliable choice:
    use coarse spine-document ranges
```

Initial substantial threshold should be tested, not hard-coded forever. A reasonable fixture starting point is >= 80 narratable words OR >= 3 prose blocks, except recognized front/back matter which may be shorter.

Coverage means candidate intervals collectively cover most narratable body content without pathological overlap.

## G. Plain-text TOC detection

A probable TOC region has many consecutive lines with several of these properties:

- short titles followed by dotted leaders;
- trailing page numbers;
- repeated chapter-like prefixes;
- high density of short non-sentence lines;
- appears near beginning;
- begins after `Contents`/`Table of Contents`/`Sumário`.

Mark the region and strongly penalize heading candidates inside it. Do not delete it from source provenance.

## H. Plain-text heading pattern clustering

Do not promote `Title Case` in isolation.

1. Generate candidates + feature vectors.
2. Group by signature: explicit keyword class, numeric pattern, casing, isolation, approximate length.
3. Reward signatures recurring at plausible intervals with substantial prose between matches.
4. Infer hierarchy from signatures and numeric nesting.
5. Require stronger score for non-explicit headings.

This global pass is the primary defense against random prose being classified as headings.

## I. PDF header/footer suppression

For each extracted line retain normalized text and geometry.

1. Divide page into top/body/bottom bands.
2. Normalize candidate line: lowercase, collapse whitespace, optionally replace page numbers/dates.
3. Cluster same/similar normalized lines across pages at similar vertical positions.
4. If a cluster appears on a significant fraction of pages, mark repeating artifact.
5. Never suppress a line solely because it repeats twice.

Page-number suppression is separate: short numeric/Roman lines near page margins with monotonically changing values.

## J. Structure diagnostics

Each parser returns warnings with codes rather than prose-only strings in the eventual implementation, e.g.:

```text
EPUB_NAV_FRAGMENT_MISSING
EPUB_NAV_OUTSIDE_SPINE
EPUB_NAV_NON_MONOTONIC
EPUB_NCX_FALLBACK
TXT_LOW_CONFIDENCE_STRUCTURE
PDF_SCANNED_NEEDS_OCR
PDF_READING_ORDER_LOW_CONFIDENCE
```

The planned `listen inspect` command can render detailed messages. Normal
playback only surfaces warnings that materially prevent reliable listening.
