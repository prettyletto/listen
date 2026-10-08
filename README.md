# listen

`listen` turns readable things into an ordered audio stream.

## Install

The supported first target is Linux x86_64. Building from source requires Go
1.23 or newer.

### Linux x86_64 binary (no Go required)

Download the latest release, verify its checksum, and install it in
`~/.local/bin`:

```sh
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
release="https://github.com/prettyletto/listen/releases/latest/download"
curl -fsSL "$release/listen-linux-amd64" -o "$tmpdir/listen-linux-amd64"
curl -fsSL "$release/SHA256SUMS" -o "$tmpdir/SHA256SUMS"
(cd "$tmpdir" && sha256sum --check SHA256SUMS)
install -Dm755 "$tmpdir/listen-linux-amd64" "$HOME/.local/bin/listen"
```

Add `~/.local/bin` to `PATH` if needed. This skips Go; first-run listening
still needs `uv`, `mpv`, and FFmpeg as described below.

### Install with Go

With Go installed, install the latest version directly:

```sh
go install github.com/prettyletto/listen/cmd/listen@latest
```

If the shell cannot find `listen`, add `$(go env GOPATH)/bin` to `PATH` (or
set `GOBIN` before running `go install`).

To install the checked-out development version, use Go and Make:

```sh
git clone https://github.com/prettyletto/listen.git
cd listen
make install
```

`make install` installs to `~/.local/bin`; choose another absolute directory
with `make install BINDIR="$HOME/bin"`. It still requires Go to compile the
program. The Go install command writes to Go's configured binary directory.

Before the first listening session, install `uv`, `mpv`, and FFmpeg, then run
`listen setup` to install and check local speech. See
[`docs/SETUP_AND_DISTRIBUTION.md`](docs/SETUP_AND_DISTRIBUTION.md) for host
requirements and PDF tools.

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

In an interactive Linux terminal, run `listen` with no arguments to open your
local listening home. Press **a** to add a file or folder path, **/** to search
recent sources, **Up/Down** to move focus, **Enter** to open the single focused
item, and **q** to exit.
The featured Continue source appears once; Recents lists the other sources.
`listen <source>` still
starts that source immediately and resumes an unfinished session. While audio
plays, use **Space** to pause/resume, **Left/Right** to seek by five seconds,
**p/n** to move between chapters, **Escape** to save and return home, and
**q** or **Ctrl-C** to save and exit. Piped and other non-interactive runs do
not open the listening home.

When local speech needs setup, select a source first, then choose **Start
recommended setup** to download the local runtime and voice files. Press **p**
from home to choose voices by language and adjust speech or playback speed.
Voice and speech-generation speed apply to the next session; `+` and `-`
change playback speed during listening. Run `listen config set playback-speed
1.2` to set a default from 0.5× to 3×.

Use `listen book.epub -o ./book-audio/` to build a reusable book bundle instead
of opening playback. It contains the full-book MP3 and M4B, separate chapter
MP3s, a chapter playlist, and a manifest. Completed speech is cached as it is
generated, so rerunning after an interruption continues without regenerating
finished audio. In an interactive terminal, Listen shows the current stage and
progress bar. During audio generation and file encoding, an activity marker
moves while the completion percentage tracks finished work. Non-interactive
runs print progress updates. `-o DIRECTORY` is shorthand for
`--export DIRECTORY`.

`listen docs/` reads supported documentation files into one ordered session.
Directory inputs honor applicable `.gitignore` rules and keep each block's
source path in the shared document model.

The command keeps document parsing, translation, local speech, caching, and
export behind a source path. `listen main.go --code` adds structured code
narration, including package structure, signatures, comments, and recognized
control flow.

## Development

The command and parser tests use fake speech/translation workers and do not
download model files:

```sh
make test
make vet
```

`make check` formats Go sources, runs the full test suite, and runs `go vet`.

PDFs are supported on a best-effort basis. PDF reading requires
Poppler's `pdftohtml` and `pdfimages`; on Arch Linux, install them with
`sudo pacman -S poppler`.
`qpdf` is optional and adds PDF bookmark navigation. Pages that appear scanned
use local Tesseract OCR when Tesseract and the configured trained language data
are installed. English is the default; set additional Tesseract language IDs
with `listen config set ocr-languages eng+por`. Normal playback begins as
pages are recognized, while `--no-play`, `--export`, and translated reading
finish OCR first and show page progress. OCR does not upload document text.
For streamed scanned-PDF playback, the listening position is saved by passage
and page. On return, Listen checks cached OCR pages, skips earlier passages,
and resumes within the saved audio passage. If the PDF changed, it starts near
the saved page at the beginning of a passage and explains the adjustment.
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
changes them, including `playback-speed`. Translation setup downloads only
model files; source text stays on this device. Choosing a voice language does
not enable translation; translation remains explicit for each source.
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

## License

Listen is licensed under the MIT License. See [LICENSE](LICENSE).
