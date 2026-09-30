# listen

`listen` turns readable things into an ordered audio stream.

The product promise is intentionally small:

```sh
listen setup
listen setup --translate en:pt-BR
listen book.epub
listen book.epub --translate pt-BR
listen README.md
listen main.go --code
listen docs/
listen book.epub --export
listen book.epub -o ./book-audio/
```

In an interactive Linux terminal, a book with chapters opens a start picker:
read the full book from the beginning, resume an unfinished session, or start
at a chapter and continue through the rest. The book title appears once above
the chapter list; press **/** to search long lists, **↑/↓** or **j/k** to move,
then **Enter** to start. While audio plays, use **Space** to pause/resume,
**Left/Right** to seek by five seconds, **p/n** to move between chapters, and
**q** or **Ctrl-C** to stop and save your place. Piped and other non-interactive
runs do not open the picker.

Use `listen book.epub -o ./book-audio/` to build a reusable book bundle instead
of opening playback. It contains the full-book MP3 and M4B, separate chapter
MP3s, a chapter playlist, and a manifest. Completed speech is cached as it is
generated, so rerunning after an interruption continues without regenerating
finished audio. `-o DIRECTORY` is shorthand for `--export DIRECTORY`.

`listen docs/` reads supported documentation files into one ordered session.
Directory inputs honor applicable `.gitignore` rules and keep each block's
source path in the shared document model.

Everything complicated lives behind those commands: structure parsing, resumable caches, translation backends, local-vs-cloud planning, Kokoro process management, ordered concurrent generation, buffering, playback, and export.

listen main.go --code opts into Go AST narration of package structure,
signatures, comments, and recognized control flow.

This repository is an implementation foundation. The Python prototype under `reference/python-prototype/` is the behavioral reference for the existing TTS/cache/section UX; the new architecture is Go-first, with Python limited to long-lived Kokoro and optional local translation inference workers.

PDFs are supported on a best-effort basis. PDF reading requires
Poppler's `pdftohtml` and `pdfimages`; on Arch Linux, install them with
`sudo pacman -S poppler`.
`qpdf` is optional and adds PDF bookmark navigation. Pages that appear scanned
use local Tesseract OCR when Tesseract and the configured trained language data
are installed. English is the default; set additional Tesseract language IDs
with `listen config set ocr-languages eng+por`. Normal playback begins as
pages are recognized, while `--no-play`, `--export`, and translated reading
finish OCR first and show page progress. OCR does not upload document text.
OCR quality depends on print and scan quality, language data, and page layout;
Listen reports low-confidence and unreadable pages instead of claiming exact
recognition. Only English currently has a committed OCR fixture. PDFs are
limited to 256 MiB, 5,000 pages, bounded extractor output, and two minutes per
external PDF tool.

On Linux x86_64, install `uv`, mpv, and FFmpeg, then run `listen setup`.
In a terminal, the setup wizard lets you select listening languages and a
default voice for each. It offers optional local English-to-Brazilian
Portuguese translation; `listen setup --translate en:pt-BR` selects it
directly. Setup installs the private Python 3.13 environment, checks local
synthesis and any selected translator, and saves choices after those checks
pass. `listen config` displays saved preferences, and `listen config set ...`
changes them. Translation setup downloads only model files; source text stays
on this device.
Use `listen doctor` to inspect setup and playback readiness, `listen voices` to
view voice choices, and `listen config` to manage preferences.

`listen book.epub --translate pt-BR` uses the installed local translator when
the EPUB declares English as its source language. Translated speech defaults
to a configured Brazilian Portuguese voice. The reader estimates whether local
translation can keep up with playback and warns when it may fall behind. If
the opening playback probe is unsustainable, `cloud-policy` determines whether
to stay local (`never`), ask first (`ask`), or use the configured OpenAI model
(`allowed`). The API key is read from `OPENAI_API_KEY` and is never saved to
config or session files. Use `listen models` to discover model IDs and
`listen config set cloud-model <id>` to select one. `--export` has no realtime
deadline.

## Non-negotiable product rules

1. The default path must remain `listen <thing>`.
2. Adding capability must not add decisions to the default path.
3. Local processing is preferred; remote processing is never silent.
4. Playback order is deterministic regardless of worker completion order.
5. Completed work is resumable and content-addressed.
6. Time-to-first-audio matters more than time-to-full-export.
7. Source structure is preserved. Do not flatten a book into anonymous chunks.
8. EPUB structure comes from EPUB semantics first, heuristics last.
9. Streaming playback and full export use the same pipeline and caches.
10. Model availability is not enough: a local model must be fast enough for the requested mode.

## Read first

Implementation agents should read, in order:

1. `AGENTS.md`
2. `docs/PRODUCT.md`
3. `docs/UX.md`
4. `docs/ARCHITECTURE.md`
5. `docs/DOCUMENT_IR.md`
6. `docs/BOOK_STRUCTURE_AND_PARSING.md`
7. `docs/SEGMENTATION.md`
8. `docs/STREAMING_PIPELINE.md`
9. `docs/IMPLEMENTATION_RUNBOOK.md`
10. `docs/VERTICAL_SLICES.md`

Then implement slices in order and do not start the next slice until its exit criteria pass.
