# UX contract

## Golden path

After one-time setup:

```sh
listen book.epub
```

Expected behavior:

1. Detect source and previous session.
2. Parse or load cached structure.
3. Resolve language and default voice.
4. In an interactive terminal, choose the whole document from the beginning,
   an unfinished saved position, or a chapter/major section. A new source
   defaults to the beginning; an unfinished session defaults to Resume.
5. Generate enough ordered audio for a safe initial buffer.
6. Start playback in the player screen and continue processing ahead.
7. Show the current chapter and position; allow pause/resume, previous/next
   chapter, and stop.
8. Persist progress and completed work.
9. On next invocation, offer the saved position and allow another start point.

No model picker appears on the golden path.

Choosing a chapter starts at its first segment and continues through the
remaining chapters. Chapter changes cancel the active stream and restart from
the selected boundary, preserving cached work and sequence ordering. In
non-interactive runs, use the saved position or start from the beginning
without prompting.

## Setup

`listen setup` owns dependency/model/voice setup. Every interactive invocation
opens a guided preferences flow; pressing Enter keeps the shown default. It
should:

- verify mpv and ffmpeg or install guidance;
- provision the private Python inference-worker environment for Kokoro and any
  explicitly selected local translator;
- show listening languages and let the user select one or more;
- download only required voices/resources;
- let the user choose a default voice for each selected language;
- offer local English-to-Brazilian-Portuguese translation, or accept the
  explicit `--translate en:pt-BR` option;
- install and health-check the optional local translator;
- measure translation viability against the opening segments of the actual
  source before translated playback;
- keep cloud fallback at `ask` by default, with `never` and `allowed` available
  through `listen config`;
- persist config.

Non-interactive setup uses saved or default choices and does not prompt.
`listen config` displays saved preferences and accepts explicit changes.
Advanced diagnostics belong in `listen doctor`.

## Commands

Required public surface:

```text
listen setup
listen <source>
listen <source> --translate pt-BR
listen <source> --export
listen voices
listen models
listen config
listen doctor
```

Useful later:

```text
listen inspect <source>
listen <source> --from "Chapter 4"
listen <source> --model translategemma-4b
--include-code
--verbose
```

`listen inspect`, `--from`, `--model`, `--include-code`, and `--verbose` are
planned controls, not currently implemented. `--voice <id>` is supported as a
one-shot override. Directory paths and `-` for standard input are already
accepted as sources.

## Progressive disclosure

`listen models` currently discovers OpenAI translation model IDs, and `listen
config` manages voice, speed, and cloud fallback settings. Local translator
discovery and worker-count controls remain future work. Normal playback should
not expose provider internals.

## Error philosophy

Default errors answer three things only:

1. What could not be done?
2. Is existing work safe?
3. What one action fixes it?

If a future verbose mode is added, it can include subprocess stderr, model
paths, HTTP status, timings, cache keys, and parser diagnostics.

## Cloud consent

Content is never sent remotely merely because local translation is slow.

Policies:

- `never`: local only; degrade to wait/export if unsustainable.
- `ask`: prompt before the first remote request for this operation; absent or
  non-interactive input means decline.
- `allowed`: configured provider may be used automatically.

The initial setup defaults to `ask` unless the user explicitly chooses otherwise.

## Translation fallback UX

A model can fit and still be useless for streaming. If local throughput is below the safety threshold, the user sees a simple decision, not token/s internals:

```text
Local translation will probably fall behind playback.
Allow OpenAI to receive this book's text for translation? [y/N]
```

The prompt names the remote action before any source text is sent. Under
`allowed`, the same switch happens without a prompt. The opening local probe
selects one provider for the whole operation, keeping every major unit on the
same backend and preserving cache/resume identity.

Realtime ratios and model details are not currently exposed through a verbose
CLI mode.

## Export terminology

Use `--export`, not `--no-stream`, as the primary user-facing concept. Streaming is an implementation mechanism; export is the user's goal.
