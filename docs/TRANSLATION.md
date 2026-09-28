# Translation architecture

Translation is optional and must not infect parser/TTS interfaces.

## Adapter families

### Local

The first shipped local backend is Argos OPUS-MT English to Brazilian
Portuguese (`en:pt-BR`), package version 1.9. Go owns orchestration and runs a
single long-lived CTranslate2 CPU worker through a versioned JSONL protocol.
The worker accepts complete segment batches and returns complete text keyed by
segment ID. It does not choose playback order. The selected voice defaults to
the configured `pt-BR` Kokoro voice.

Install it explicitly with `listen setup --translate en:pt-BR`. The installer
downloads the pinned `.argosmodel` archive, checks its SHA-256, extracts only
the model/tokenizer files, and atomically commits the package under the XDG
data directory. It never downloads or uploads book text. The setup health check
starts the persistent worker and validates its language-pair capability.

Only English sources and Brazilian Portuguese targets are supported in this
slice. The source locale comes from EPUB metadata or Markdown front matter; an
undeclared or unsupported source language fails before translation or speech
starts. The model package README requests CC-BY 4.0 attribution. See [the
benchmark and attribution record](TRANSLATOR_BENCHMARK.md).

### Cloud

The first direct provider is OpenAI's Responses API. It sends complete batches
with strict JSON Schema output, `store:false`, and no token streaming. Every
request carries the target locale, bounded context, style instruction, and
glossary so terminology remains consistent across major units. Model IDs can
be discovered with `listen models` and selected with
`listen config set cloud-model <id>`.

The adapter reads its API key from `OPENAI_API_KEY`, uses cancellable Go HTTP
requests, and does not log response bodies. It has no retry loop, preventing an
automatic retry from duplicating a billed request. The API key is never saved
in local configuration or session state.

### Runner

Advanced adapters that invoke Codex/OpenCode/Claude/Gemini CLI or a user-defined executable. These are useful but less predictable and should not be the default backend when a direct API exists.

## Interface

A translator receives complete semantic segments/batches and returns complete text for the same IDs. It must not decide playback order.

The Slice 6 pipeline batches at most 16 segments and keeps each batch within
one major unit and one source locale. Translation results are validated as
complete UTF-8 text, matched back to stable segment IDs, and reordered by
sequence before they enter speech generation. Translation and speech caches
are committed independently so a rerun can reuse either stage.

Translation cache identity includes the source text hash, source and target
locales, backend ID, prompt version, and the fingerprint of the bounded
context artifact. The artifact holds the glossary, style instruction, and
optional summary. The current limits are 128 glossary entries, 256 characters
per glossary term, 4,000 characters for a summary, and 1,000 characters for a
style instruction. A translation result is capped at 1 MiB.

The command rejects a raw remote translator before source access, and the
stream requires an authorization wrapper before accepting a remote adapter.
The wrapper is created only after `allowed` policy or a positive `ask`
confirmation. The `never` policy and a declined prompt never construct the
remote adapter.

## Translation consistency

For books maintain per-session context:

- proper-name glossary;
- user glossary;
- recurring technical terms;
- target locale (`pt-BR`, not generic “Portuguese” when the backend supports it);
- concise style instruction;
- optional rolling summary of prior context.

Do not put the entire prior book into every prompt. Persist a bounded context artifact.

## Determinism

Cloud/LLM translation is not byte-deterministic. `listen` determinism means:

- source segment boundaries are deterministic;
- results are attached to correct IDs;
- committed results do not change mid-playback;
- cache keys include provider/model/prompt/glossary/context-version;
- playback order is deterministic.

## Cloud privacy

Remote adapters declare that they are remote. The app checks the configured
cloud policy before creating the authorization wrapper or allowing the stream
to call the provider. `ask` requires interactive approval; non-interactive
invocations decline safely.

## Auto selection

`auto` is a planner, not a model.

Inputs:

- installed/configured backends;
- language-pair capability;
- privacy policy;
- streaming vs export;
- measured local realtime ratio;
- known provider availability.

Output: one backend pinned for the full operation according to policy. The
opening local playback probe runs before playback starts. If it is
unsustainable, `allowed` selects the configured cloud adapter automatically;
`ask` prompts; and `never` continues locally with a warning. The selected
provider/model stays fixed for all major units in that operation, preserving
translation cache and resume identity. Export does not use a realtime cutoff.
