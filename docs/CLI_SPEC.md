# CLI specification

The CLI is intentionally narrow. Advanced capability is discoverable without contaminating the default path.

## Core

```text
listen <source>
listen <source> --translate <BCP-47>
listen <source> --export [directory]
listen setup
listen setup --translate en:pt-BR
```

`<source>` may eventually be a file, directory, or `-` for stdin. File type is inferred from content/extension; users do not choose parsers. The current reader supports UTF-8 plain-text files, Markdown files, and EPUB books.

## Discovery

```text
listen voices
listen models
listen config
listen doctor
listen inspect <source>
listen --help
```

`listen config` displays preferences. Mutations are explicit, for example
`listen config set voice pf_dora`, `listen config set voice pt-BR pf_dora`,
`listen config set speed 1.1`, `listen config set cloud-policy never`, and
`listen config set cloud-model gpt-5.6-luna`.

## Optional one-shot overrides

```text
--voice <id>
--model <id>
--speed <speech-rate>
--playback-speed <rate>
--no-play
--from <chapter-or-section>
--include-code
--verbose
```

Do not require these for normal use.

`--no-play` generates and caches audio without opening mpv; it is useful for
automation and validating local speech setup.

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

## `listen inspect`

This is the parser/model transparency tool. It may show:

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

`listen models` separates:

- automatic planner;
- installed local translators;
- available local translators;
- configured cloud providers;
- runner adapters.

`listen voices` groups installed/available voices by locale and marks the default. It should not dump every low-level Kokoro artifact by default.

The first `listen setup` asks for a listening language, installs the private
Linux x86_64 speech runtime, and confirms it by generating a short local sample.
Run `listen doctor` to inspect runtime, player, export, config, and model-cache
readiness. The available voice list is curated; model files are fetched by
Kokoro into the local cache when setup checks a configured language.
