# EPUB structure fixtures

- `nested.epub`: EPUB 3 nested nav; nav page is also in spine; front matter, Part, chapters, fragment subsections, appendix, footnote.
- `split_chapter.epub`: Chapter 1 spans two spine XHTML files, proving `file != chapter`.
- `multi_chapter_one_file.epub`: two chapter nav targets point into different fragments of the same XHTML file.
- `ncx_fallback.epub`: EPUB 2 with NCX and no EPUB 3 navigation document.
- `broken_fragment`: a generated EPUB test case whose navigation points at a missing chapter fragment.
- `nonlinear_navigation`: a generated EPUB test case whose TOC points into a non-linear spine resource.
- `broken_navigation`: a generated case with a missing resource and an unsupported linear spine target.

The parser tests compare the four checked-in archives and all generated cases
against full Document IR goldens in `testdata/expected/epub/`.

The golden corpus asserts:

1. canonical block order always follows spine;
2. nested nav hierarchy remains nested;
3. nav fragment targets map to correct block starts;
4. chapter ranges cross file boundaries when appropriate;
5. chapter ranges split one file when appropriate;
6. nav/TOC page is skipped by default when it appears in the spine;
7. NCX creates high-confidence hierarchy with a fallback warning/diagnostic;
8. footnote is retained as a note block but excluded from default prose interruption;
9. a missing navigation fragment maps to the target document start and adds a warning;
10. non-linear targets retain title, parent path, source target, and provenance without entering reading order;
11. missing or unreadable linear targets receive actionable warnings and are not mislabeled as auxiliary.
