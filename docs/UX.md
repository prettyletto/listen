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
4. Generate enough ordered audio for a safe initial buffer.
5. Start playback.
6. Continue processing ahead of playback.
7. Persist progress and completed work.
8. On next invocation, resume naturally.

No model picker appears on the golden path.

## Setup

`listen setup` owns dependency/model/voice ugliness. It should:

- verify mpv and ffmpeg or install guidance;
- provision the private Python inference-worker environment for Kokoro and any
  explicitly selected local translator;
- ask which listening languages the user wants;
- download only required voices/resources;
- configure a default voice per language;
- optionally install/configure one local translator;
- benchmark local translation capability;
- store cloud fallback policy: `ask` by default, with `never` and `allowed`
  available through `listen config`;
- persist config.

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
listen inspect <source>
```

Useful later:

```text
listen <source> --from "Chapter 4"
listen <source> --voice dora
listen <source> --model translategemma-4b
listen <directory>
cat notes.md | listen -
```

## Progressive disclosure

`listen models` may reveal local/cloud models. `listen config` may expose worker counts and fallback policies. Normal playback should not.

## Error philosophy

Default errors answer three things only:

1. What could not be done?
2. Is existing work safe?
3. What one action fixes it?

Verbose mode can include subprocess stderr, model paths, HTTP status, timings, cache keys, and parser diagnostics.

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

`--verbose` may show realtime ratios and model details.

## Export terminology

Use `--export`, not `--no-stream`, as the primary user-facing concept. Streaming is an implementation mechanism; export is the user's goal.
