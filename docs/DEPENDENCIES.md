# Dependency candidates

These are candidates, not permission to couple domain logic to them.

## EPUB

`github.com/raitucarp/epub` currently advertises EPUB 3.3 reading, including OCF, package, navigation document, and legacy NCX support. Evaluate it in the EPUB slice against our fixtures. If it cannot expose exact target/DOM details needed by the IR, use it for container/package metadata and parse XHTML/navigation ourselves.

## Markdown

Goldmark is a mature CommonMark parser with an AST and source positions. Prefer stable v1 initially; v2 is still described upstream as early-stage.

## HTML

`golang.org/x/net/html` provides an HTML5 tokenizer/parser suitable for DOM extraction.

## PDF

Candidates should be benchmarked against the golden PDF corpus. A current pure-Go option, `github.com/giraffesyo/pdf`, exposes positioned glyphs and reading-order reconstruction. `pdfcpu` can handle PDF bookmarks/outlines and metadata, but PDF parsing is a risk area and must be resource-limited.

## Playback

mpv stable manual documents JSON IPC and playlist `loadfile` append modes. Use it as an external player initially.

## Rule

Do not select dependencies by GitHub popularity alone. Add a small adapter and fixtures first, then pin only after passing the project-specific requirements.
