# Documentation directories

`listen <directory>` builds one ordered session from regular Markdown,
plain-text, HTML/XHTML, and PDF files beneath the selected directory. Other
file types are ignored. EPUB remains a direct source. Directory traversal
skips `.git`, `.hg`, `.svn`, and symbolic links; an explicitly selected
directory symlink is resolved before traversal.

## Ignore rules

The resolver reads `.gitignore` files from the containing Git worktree root
through the selected directory, then from each visited subdirectory. A nested
file's rules have higher precedence than its parent's rules, and the last
matching rule decides whether a path is ignored. It supports comments,
negation, escaped leading `#` and `!`, anchored and directory-only patterns,
`*`, `?`, character classes, and `**`. Negated files are reachable only when
their parent directories are not ignored, matching Git's traversal behavior.
Global Git excludes and `.git/info/exclude` do not affect a reading session.

## Reading order

Eligible files are sorted by this policy, then by case-insensitive relative
path and finally by the original relative path to break ties:

1. A README at the selected directory root.
2. An `AGENTS.md` at the selected directory root.
3. Nested README files, including a `docs/README.md` index.
4. Nested `AGENTS.md` files.
5. ADR files in an `adr/` path or with an `adr-`, `adr_`, or `adr ` prefix.
6. Other files below a `docs/` directory, or all remaining files when the
   selected directory itself is named `docs`.
7. Other supported documentation files.

Files within each priority group use the deterministic path sort. Directory
enumeration order and file creation order do not affect the session.

## Composite document

Each eligible file is parsed by its existing format parser. The composite
document has a directory-level book outline, followed by each file's outline
in session order. Block sequence numbers become contiguous across the session;
outline intervals are shifted to cover the combined block stream. Each file is
a segment-planning boundary, so one spoken segment does not combine text from
two source files. Block and outline IDs include the relative source path so
repeated files remain unique.
Each block retains the underlying absolute source path and source offsets or
format-specific locator. The composite identity is derived from the ordered
relative paths and parsed document identities, so content or ignore-rule
changes that alter the session invalidate its structure and segment plan.
