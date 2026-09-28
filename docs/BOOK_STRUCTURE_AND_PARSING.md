# Book structure and parsing

This is the most important technical specification in the repository.

The parser must preserve author/publisher structure when the format provides it and infer structure only when it does not.

---

# 1. Universal parser rule: authoritative structure first

Use this precedence:

1. Format-native reading order and navigation metadata.
2. Format-native semantic elements/headings.
3. Layout/typographic evidence.
4. Textual heuristics.

Never let a weaker source override a stronger one without recording a repair warning.

All parsers output the same `Document` IR: a canonical ordered block stream plus a navigation tree mapped to exact block ranges.

---

# 2. EPUB — first-class, not “ZIP + strip HTML”

EPUB must be the best-supported book format.

## 2.1 Canonical sources of truth

An EPUB provides two distinct truths:

- **OPF spine**: default linear reading order.
- **EPUB navigation document (`nav epub:type="toc"`)**: human navigation hierarchy.

For EPUB 2, NCX is the navigation fallback.

Use the spine to order content documents. Use navigation/NCX to define parts/chapters/sections. Then map nav targets into the spine-derived block stream.

### Precedence

1. EPUB 3 navigation document.
2. EPUB 2 NCX.
3. XHTML semantic headings/`epub:type`.
4. Spine document boundaries/titles.
5. Text heuristics as last resort.

## 2.2 EPUB open algorithm

1. Open EPUB as an OCF ZIP container.
2. Read `META-INF/container.xml`.
3. Resolve the package document (`.opf`).
4. Parse metadata: title, creators, language, identifier.
5. Parse manifest resources.
6. Parse spine itemrefs in order.
7. Respect `linear="no"`: keep provenance but exclude from default reading flow unless needed by navigation.
8. Resolve EPUB 3 nav item (`properties` contains `nav`).
9. If absent/unusable, resolve legacy NCX.
10. Parse each spine XHTML document into semantic blocks.
11. Build a target index mapping `path#fragment` to the first relevant block position.
12. Parse navigation tree and map every target to a block position.
13. Derive half-open ranges for every navigation node.
14. Classify roles and major units.
15. Validate invariants; repair conservatively and emit warnings.

## 2.3 XHTML block extraction

Do not flatten XHTML to one string first.

Walk the DOM in reading order and emit blocks for:

- `h1`..`h6` -> heading
- `p` -> paragraph
- `li` -> list item
- `blockquote` -> quote
- `pre`/block code -> code
- table rows/cells -> table-row representation
- captions -> paragraph/semantic caption
- explicit section breaks -> break
- footnote/endnote bodies -> note

Ignore by default:

- script/style/template
- hidden nodes (`hidden`, appropriate `aria-hidden`)
- navigation chrome
- page-break markers
- empty decorative elements

Preserve element IDs and nearby ancestor IDs so fragment links can resolve to block positions.

### Inline normalization

Within a block:

- concatenate inline text with whitespace normalization;
- preserve sentence punctuation;
- use image `alt` only when it conveys textual content and is not decorative;
- links contribute anchor text, not URLs by default;
- superscript note markers are omitted when they are only noteref markers;
- MathML uses accessible/alt text when present; otherwise best-effort textual content.

## 2.4 Navigation target mapping

Normalize hrefs relative to the navigation document.

A target may be:

- `chapter3.xhtml`
- `chapter3.xhtml#start`
- `chapter3.xhtml#section-2`

Resolve to a canonical `(spine document, fragment)`.

Mapping rules:

1. Exact element ID -> first narratable block at or after that element.
2. ID on a container -> first descendant narratable block.
3. No fragment -> first narratable block in that spine document.
4. Missing fragment -> document start plus warning.
5. Target outside linear spine -> retain as navigation metadata, but do not silently splice auxiliary content into reading order unless explicitly selected.

Create a fast index while parsing XHTML rather than rescanning DOMs.

## 2.5 Turning navigation starts into ranges

Navigation gives start points, not necessarily end points.

Given a tree node N:

- `StartBlock` is its mapped target.
- `EndBlockExclusive` is the first following navigation node in document order whose depth is **less than or equal to N's depth**, or the end of the parent's range/document if none exists.
- A parent naturally spans all descendants.

When parent and first child share the same target, overlapping parent/child ranges are valid because they are ancestor/descendant. Siblings with the same target are suspicious: preserve labels if necessary but mark the later node as an alias/repair candidate rather than inventing text.

After range derivation run validation:

- starts are monotonic in preorder where expected;
- children are contained;
- no sibling inversions;
- end <= number of blocks.

If nav ordering conflicts badly with the spine, **spine wins linear reading order**. Keep the nav tree where possible but record a warning.

## 2.6 Navigation enrichment with content headings

EPUB navigation documents are often intentionally shallow. A publisher may expose only chapters in the TOC while the chapter XHTML contains useful `h2`/`h3` subsections. We want those subsections without weakening nav authority.

After authoritative nav/NCX ranges are mapped:

1. Parse XHTML heading blocks and semantic section headings.
2. For each authoritative navigation node, inspect heading blocks strictly inside its range.
3. If a heading start already corresponds to a nav descendant (same target/start, or strongly matching title at the same location), **merge** metadata instead of creating a duplicate node.
4. Otherwise add it as an inferred child under the nearest authoritative/inferred ancestor whose range contains it.
5. Normalize heading depth relative to the chapter/root heading. A chapter targeted at an `h1` followed by `h2`/`h3` naturally becomes chapter -> section -> subsection.
6. A skipped heading level (`h1` -> `h3`) does not require inventing a missing node; attach the `h3` beneath the nearest valid ancestor while preserving relative depth metadata.
7. Never allow an inferred heading to move the start/end boundary of an authoritative nav sibling or reorder spine content. It only subdivides an existing range.
8. If heading structure conflicts with nav boundaries (for example a heading visually appears before the nav node that supposedly contains it), nav/spine boundaries win and a diagnostic is recorded.

This produces a hybrid tree: authoritative navigation for global structure plus high-confidence semantic headings for missing internal detail. Track node provenance internally (`nav`, `ncx`, `xhtml-heading`, `heuristic`) even if it is not part of the first public IR schema.

## 2.7 Role classification

Use strongest evidence first:

### Semantic evidence

EPUB/HTML semantics may identify front matter, body matter, chapter, appendix, bibliography, glossary, index, notes, etc. Preserve these semantics on blocks/nodes.

### Title evidence

Recognize labels such as:

- Part / Parte / Book / Livro / Volume
- Chapter / Capítulo
- Prologue / Prólogo
- Epilogue / Epílogo
- Preface / Foreword / Introduction
- Appendix / Apêndice
- Glossary / Bibliography / References / Index

### Structural evidence

If a Part node contains many same-depth children that cover substantial body text, those children are likely major units even when titles are unusual.

## 2.8 Major-unit selection for playback/export

Do **not** simply pick top-level TOC entries.

Algorithm:

1. Exclude pure navigation/cover/copyright/TOC nodes from candidate tracks by default.
2. Preserve meaningful front matter (preface, foreword, introduction) as units.
3. If explicit chapter-role nodes exist, use them.
4. Else if Part nodes have 2+ substantial children, use those children while retaining Part labels in their section path.
5. Else use the shallowest outline depth that yields at least 2 substantial, nontrivial units.
6. Appendices/epilogue/conclusion become units.
7. If no reliable hierarchy exists, use spine documents as coarse units rather than guessing fake chapters.

“Substantial” should be based on narratable word/block count, not filename length.

## 2.9 The table-of-contents page problem

The EPUB nav document can itself appear in the spine. Narrating it often creates a useless duplicate list of chapter names.

Default policy:

- keep the nav document in provenance;
- mark the TOC navigation block/page `SkipByDefault` when its dominant content is a table of contents;
- do not skip an ordinary chapter merely because it contains navigation links.

Use semantic `epub:type=toc` first, then density heuristics as fallback.

## 2.10 Footnotes/endnotes

Default listening policy:

- omit inline note reference numbers;
- do not interrupt prose to read footnote bodies inline;
- retain notes as blocks with semantics and source targets;
- later modes may support “read notes at end of section/chapter”.

This prevents terrible default audiobook flow while preserving data.

## 2.11 EPUB split-file chapters

A chapter can span multiple XHTML spine files. Conversely, one XHTML file can contain several chapters.

Therefore **file != chapter**.

Navigation ranges mapped onto the global block stream solve both cases. Never derive chapter identity from filenames alone.

## 2.12 Broken EPUB repair strategy

Real EPUBs are imperfect.

Repair in descending authority:

- nav target missing fragment -> map to document start;
- nav references document not in linear spine -> retain auxiliary target, do not mutate linear order silently;
- missing nav -> NCX;
- missing/broken NCX -> semantic headings;
- no headings -> spine boundaries;
- only then title heuristics.

Every repair produces a machine-readable warning visible in `listen inspect`/verbose diagnostics, not noisy default playback.

---

# 3. Markdown

Use a real CommonMark AST, not regexes.

Recommended parser: Goldmark (stable v1 unless v2 ecosystem is proven for required extensions).

## Extraction

- heading AST nodes define high-confidence outline nodes;
- paragraphs/list items/quotes become blocks;
- fenced/indented code becomes `BlockCode` and is skipped by default;
- tables become row-oriented blocks;
- YAML/TOML front matter is metadata/skipped prose unless intentionally requested.

Normalize heading depth relative to observed document structure rather than requiring an H1.

For a Markdown “book”, major-unit classification uses the same role/structure algorithm as EPUB after the AST outline is mapped to blocks.

---

# 4. Plain text

Plain text has no structural authority, so inference must be conservative and scored.

## 4.1 Pre-cleaning

Before heading detection:

- normalize newlines and Unicode whitespace;
- detect probable Project Gutenberg boilerplate and mark it optional/skipped (never delete without provenance);
- detect probable table-of-contents regions so TOC entries are not mistaken for body chapter headings;
- preserve blank-line boundaries.

## 4.2 Candidate heading score

Each line receives evidence, for example:

Positive:

- isolated by blank lines;
- `CHAPTER`, `CAPÍTULO`, `PART`, etc.;
- Roman/Arabic ordinal pattern;
- short line;
- all-caps/title-case;
- followed by substantial prose;
- same pattern repeats throughout document;
- page/form-feed boundary nearby.

Negative:

- terminal sentence punctuation;
- very long line;
- looks like dialogue/list prose;
- many similar lines clustered in a probable TOC;
- email/URL/code-like punctuation;
- no surrounding whitespace.

Do not assign hierarchy one line at a time. First collect candidates, then infer repeated patterns across the whole document.

## 4.3 Global pattern inference

Examples:

- repeated `CHAPTER <n>` lines -> chapter level;
- repeated `1.2 Something` -> depth based on dotted numeric components;
- `PART I` followed by reset chapter numbers -> Part parent;
- isolated uppercase short lines between chapter markers -> likely section headings, but lower confidence.

If confidence remains low, prefer fewer/coarser sections. Incorrect chapter boundaries are worse than a long continuous unit.

---

# 5. Generic HTML

Generic HTML differs from EPUB XHTML because websites contain chrome.

Selection precedence:

1. `<main>`
2. dominant `<article>`
3. body fallback

Suppress navigation/header/footer/sidebar/script/style. Use `h1`..`h6` for outline, semantic section/article tags as supporting evidence, not as automatic headings.

Do not apply aggressive article-extraction heuristics to EPUB content documents.

---

# 6. PDF

PDF is explicitly best-effort. A PDF describes page painting, not necessarily logical reading structure.

## Evidence precedence

1. Tagged-PDF structure/tree when available.
2. PDF document outline/bookmarks.
3. Positioned text + typography/layout analysis.
4. Plain extracted text heuristics.
5. OCR only in a later optional capability.

## Reading-order reconstruction

Use positioned glyph/line data where possible. Required steps:

- reconstruct words/lines;
- detect columns before joining lines;
- preserve page transitions as metadata, not spoken page numbers;
- remove repeated headers/footers by normalized-text frequency and consistent page position;
- suppress standalone page numbers near margins;
- preserve paragraph breaks from geometry/indentation.

## Heading inference

Candidate features:

- font size relative to page/body median;
- font weight/style where available;
- whitespace above/below;
- centering;
- short text;
- numbering/title patterns;
- bookmark alignment;
- repeated typographic class across pages.

Infer heading classes globally. For example, 18pt bold centered lines occurring 20 times are stronger evidence than one anomalous large line.

## Scanned PDF detection

If pages contain almost no extractable text but substantial raster content, mark `needs_ocr`. Do not return garbage as if parsing succeeded. v1 can provide an actionable error/diagnostic; OCR is a later adapter.

---

# 7. Structure validation shared by all formats

After parsing:

1. Ensure block sequence is contiguous.
2. Normalize empty/whitespace blocks.
3. Resolve outline starts to blocks.
4. Derive ranges.
5. Validate nested interval invariants.
6. Classify semantic roles.
7. Select major units.
8. Compute structure confidence.
9. Store parser warnings.
10. Produce deterministic structure hash.

`listen inspect <source>` should eventually expose the resulting tree and warnings so parser failures are debuggable without polluting normal playback.

---

# 8. Golden corpus required before claiming format support

At minimum include fixtures for:

### EPUB

- EPUB 3 nested Part -> Chapter -> Section nav.
- Fragment-targeted sections in one XHTML.
- One chapter spanning multiple spine documents.
- Multiple chapters in one spine document.
- EPUB 2 NCX fallback.
- nav page in spine.
- broken nav fragment.
- `linear=no` auxiliary content.
- front matter + appendix/back matter.

### Markdown

- skipped heading levels;
- code fences containing fake headings;
- lists/tables/quotes;
- Part/Chapter hierarchy.

### TXT

- explicit English chapters;
- explicit Portuguese chapters;
- Roman numerals;
- Parts with chapter numbers resetting;
- false-positive prose/title-case lines;
- embedded table of contents.

### PDF

- bookmarks;
- two-column text;
- repeated headers/footers;
- no-text scanned file detection.

No parser is “done” based on one happy-path sample.
