# CLI specification

The CLI is intentionally narrow. Advanced capability is discoverable without contaminating the default path.

## Core

```text
listen <source>
listen main.go --code
listen <source> --translate <BCP-47>
listen <source> --export [directory]
listen <source> -o <directory>
listen setup
listen setup --translate en:pt-BR
```

`<source>` may be a file, directory, or `-` for stdin. File type is inferred
from content/extension; users do not choose parsers. The reader supports
UTF-8 plain-text files, Markdown, HTML, EPUB books, best-effort PDFs (including
local OCR for scanned pages when the configured Tesseract data is installed),
and documentation directories. Go source files are narrated with the explicit
`--code` option. Directory behavior is specified in
[`DIRECTORY_SOURCES.md`](DIRECTORY_SOURCES.md).

## Discovery

```text
listen voices
listen models
listen config
listen doctor
listen --help
```

`listen config` displays preferences. Mutations are explicit, for example
`listen config set voice pf_dora`, `listen config set voice pt-BR pf_dora`,
`listen config set speed 1.1`, `listen config set playback-speed 1.2`,
`listen config set cloud-policy never`, and
`listen config set cloud-model gpt-5.6-luna`. Set local PDF OCR languages with
`listen config set ocr-languages eng+por`; the corresponding Tesseract language
data must already be installed.

## Optional one-shot overrides

```text
--voice <id>
--speed <speech-rate>
--playback-speed <rate>
--no-play
--code
```

Do not require these for normal use.

The following controls remain planned and are not accepted by the current
command parser: `listen inspect <source>`, `--model <id>`, `--from
<chapter-or-section>`, `--include-code`, and `--verbose`.

--code opts into Go AST narration for .go files. It speaks package and import
overviews, type and function signatures, doc comments, and concise summaries
of recognized control flow. Without this flag, source detection keeps its
normal reader behavior.

`--no-play` generates and caches audio without opening mpv; it is useful for
automation and validating local speech setup. For scanned PDFs it completes
OCR of the book first and reports recognized-page progress.

With no arguments in an interactive Linux terminal, `listen` opens the local
listening home. The latest unfinished source appears as one prominent Continue
action and is omitted from Recents. Exactly one item is focused; Enter opens
that item. Up/Down move between Continue and the recent-source list; `/` searches
the recents. A reliable chapter or PDF page is labeled “Saved at …”; otherwise
the home uses generic saved-place copy. `a` enters or pastes a path, `b` browses
from the current directory, `s` opens local speech setup or repair, `p` opens
listening settings, and `q` exits. The
filesystem browser opens files and navigates folders with Enter, selects the
current folder as a documentation source with `f`, toggles hidden files with
`.`, and goes up with Backspace. Missing recent sources offer Locate, Remove
from home, or Back; `u` undoes a removal. `?` shows contextual keyboard help.
The home saves source references under XDG data storage without copying files.
`listen <source>` starts immediately and resumes an unfinished session. During
playback, Space pauses/resumes, Left/Right seeks by five seconds, `p`/`n` moves
between primary chapter stops when available, and `c` opens the chapter browser.
The browser shows primary stops first; `/` searches the full outline, including
nested sections, and Enter starts at the selected boundary. Flat chapter lists
keep their existing entries. Chapter browsing pauses playback and Escape
restores its prior playing/paused state. Escape saves and returns home, and `q`
or Ctrl-C saves and exits.
The `+` and `-` keys change playback speed immediately and save the new default.
During streamed scanned-PDF recognition, the player hides unavailable chapter
controls; pressing one explains that recognition is still running. Non-terminal
runs do not prompt and keep the normal resume behavior. No-argument non-terminal
runs continue to show usage. The displayed clock is passage time, not a chapter
or book duration.

Press `s` on the home to set up or repair local speech without selecting a
source; completion returns to the home. When a selected source needs setup,
**Start setup** explicitly begins local downloads, Customize opens preferences,
and Back returns to the home. Setup progress can be cancelled and keeps that
selected source for retry. After source-first setup succeeds, Listen starts or
resumes the source. Press `p` on the home to edit the default voice,
language-specific voice mappings, speech-generation speed, or playback speed.
The settings screen reports local speech readiness and offers setup/repair. Its
Advanced settings screen edits PDF scan languages and translation privacy: ask
before sending text for translation, keep translation local, or allow remote
translation without another prompt. A local voice preview uses a fixed short
sample and stops when cancelled or when the user navigates away. Voice and
speech-generation changes apply next session; playback speed can be changed
during the current session. Explicit `--voice`,
`--speed`, and `--playback-speed` flags override saved defaults. Choosing a
voice language never enables translation; translation remains a source option
and cloud use still follows the saved consent policy.

For normal playback of a scanned PDF, Listen OCRs scan-like pages locally in
page order and starts speech once the opening audio buffer is ready. OCR
continues while audio plays. Page progress appears in the player. PDF chapter
navigation is unavailable during this streamed pass because the complete
outline is not known yet. Stopping keeps completed OCR pages and audio in the
cache; starting again reuses them, but streamed PDF playback currently starts
from the saved passage when one is available. Restoring the location traverses
cached OCR pages and can be cancelled with Escape. If PDF content changed,
Listen continues near the saved page from the beginning of that passage and
reports the adjusted location. `--translate` and `--export` finish OCR before
translation or export generation. English is the default OCR language. Other languages need
matching local Tesseract trained data, and current OCR quality fixtures cover
English only. Results vary with scan quality and layout.

`--export [directory]` builds a reusable audiobook bundle without opening
mpv. `-o <directory>` is its concise form, for example
`listen book.epub -o ./book-audio/`. With no directory,
`listen book.epub --export` writes `book-audio/` next to the source. The bundle
contains a full-book MP3, a chapter-aware M4B, separate chapter MP3s, a chapter
playlist, and a manifest. Export uses the same ordered segment plan and speech
cache as playback. If generation is interrupted, completed audio stays cached;
running the same command continues without regenerating finished segments.
In an interactive terminal, export opens a progress screen immediately with a
starting state and overall progress bar. It shows the active audio segment,
full-book MP3 creation, per-chapter tracks, chapter-aware M4B creation, and
finalization. The activity marker moves while synthesis or encoding is running;
the percentage tracks completed work. Press Esc or q to cancel. Non-interactive
runs print the same progress stages as text.
The selected output directory contains `current.json` and a versioned build:
`builds/<build-id>/full-book.mp3`, `full-book.m4b`, `all-chapters.m3u`,
`chapters/*.mp3`, and `manifest.json`. The command prints the completed build
directory when the export is ready.

`--translate <BCP-47>` identifies the target locale. Slice 7 currently ships
one local pair: English to Brazilian Portuguese (`en:pt-BR`). EPUB language
metadata or Markdown front matter must identify the source as English. Run
`listen setup --translate en:pt-BR` to install the pinned local model and
runtime. Translated listening uses the configured Brazilian Portuguese voice
by default. Startup and live throughput use playable-audio estimates and
actual generated WAV durations; playback warns when local processing may fall
behind. When its opening probe is unsustainable, `cloud-policy` controls
fallback: `never` remains local, `ask` prompts before sending text, and
`allowed` can select the configured OpenAI model automatically. The provider
and model stay fixed for the full operation. Set `OPENAI_API_KEY` in the
environment; the key is never saved by `listen config`. `listen models`
discovers model IDs and does not upload document content. Export uses the same
ordered pipeline and caches without a playback deadline.

## Exit behavior

- Ctrl-C during playback persists position and exits cleanly.
- Ctrl-C during export preserves completed cache and leaves the previous
  `current.json` build pointer in place; incomplete temporary output is removed
  when the command can handle cancellation.
- Missing mpv is an actionable setup/doctor error for playback, not a lost generation.
- `--export` may run without mpv.

## Planned: `listen inspect`

This parser/model transparency command is not available yet. When implemented,
it may show:

```text
Title: Example Book
Format: EPUB 3
Language: en
Structure: high confidence

Preface
Part I
  Chapter 1
    Section 1.1
    Section 1.2
  Chapter 2
Appendix A

Warnings:
  - TOC page excluded from default narration
```

It should also support `--json` eventually so parser regression fixtures can be generated/debugged.

## Model/voice listing

`listen models` currently discovers OpenAI translation model IDs only; it does
not list local translator backends or runner adapters, and it sends no source
text. `listen voices` lists the supported Kokoro voices by locale and marks the
default and configured voices. Voice files are downloaded locally when setup
checks the selected language.

Listing local translator and planner capabilities remains future work.

Each interactive `listen setup` invocation opens a guided preferences flow.
Choose one or more listening languages, a default voice for each language,
and whether to install the optional local English-to-Brazilian-Portuguese
translator. `listen setup --translate en:pt-BR` selects that translator without
the optional prompt. Choices are saved only after runtime and requested
language health checks pass. Non-interactive setup uses saved or default
choices without prompting. Run `listen config` to view preferences or
`listen config set ...` to change one directly. `listen doctor` inspects
runtime, player, export, config, and model-cache readiness. The available
voice list is curated; Kokoro fetches needed model files into the local cache
when setup checks each selected language.
