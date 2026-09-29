# Current implementation status

This file's original scaffold snapshot is obsolete. The planned vertical slices
0–12 are implemented and their completion and review evidence is recorded in
[`docs/IMPLEMENTATION_PROGRESS.md`](docs/IMPLEMENTATION_PROGRESS.md).

The current product includes local speech playback and export, resume and
caching, EPUB/Markdown/TXT/HTML/PDF/directory parsing, local English-to-
Brazilian-Portuguese translation, explicit OpenAI fallback policy, and
opt-in Go AST narration.

Known scope limits include Linux x86_64 for managed runtime setup, best-effort
PDF structure without OCR, one local translation pair, and no currently
available `listen inspect` command or `--model`, `--from`, `--include-code`, or
`--verbose` one-shot flags. See [`docs/CLI_SPEC.md`](docs/CLI_SPEC.md) for the
implemented and planned CLI surfaces.
