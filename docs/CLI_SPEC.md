# CLI specification

The CLI is intentionally narrow. Advanced capability is discoverable without contaminating the default path.

## Core

```text
listen <source>
listen main.go --code
listen <source> --translate <BCP-47>
listen <source> --export [directory]
listen setup
listen setup --translate en:pt-BR
```

`<source>` may be a file, directory, or `-` for stdin. File type is inferred
from content/extension; users do not choose parsers. The reader supports
UTF-8 plain-text files, Markdown, HTML, EPUB books, text-based PDFs, and
documentation directories. Go source files are narrated with the explicit
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
`listen config set speed 1.1`, `listen config set cloud-policy never`, and
`listen config set cloud-model gpt-5.6-luna`.

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
automation and validating local speech setup.

In an interactive Linux terminal, `listen <book>` opens a start picker when
the source has multiple playable chapters or major sections. Choose the full
book from the beginning, an unfinished saved position, or a chapter/section.
Starting at a chapter continues through the rest of the book. During playback,
Space pauses/resumes, Left/Right or `p`/`n` changes chapter, and `q` or Ctrl-C
stops and saves the position. Non-interactive runs do not prompt and keep the
normal resume behavior.

`--export [directory]` builds a reusable audiobook bundle without opening
mpv. With no directory, `listen book.epub --export` writes `book-audio/` next
to the source. The bundle contains a full-book MP3, a chapter-aware M4B,
separate chapter MP3s, a chapter playlist, and a manifest. Export uses the
same ordered segment plan and speech cache as playback; completed audio is
reused on later runs.

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
