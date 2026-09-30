# Architecture

## Ownership

Go owns the product:

- CLI and setup
- source detection
- all document parsing
- structure inference
- segmentation
- translation orchestration
- cache/session manifests
- concurrency/backpressure
- playback control
- export
- provider/model selection
- capability benchmarking

Python is limited to long-lived inference workers: Kokoro speech and the
optional local translation engine. Go remains responsible for orchestration,
validation, caching, ordering, playback, and export.

## Canonical flow

```text
Source
  -> Parser
  -> Document IR (ordered semantic blocks + hierarchical outline)
  -> Segment Planner
  -> optional Transform/Translation
  -> Ordered Commit Gate
  -> Speech Queue
  -> Kokoro Worker
  -> Ordered Audio Gate
  -> Content-addressed Audio Cache
  -> Playback Buffer -> mpv
                    \-> Export Sink
```

## Why an IR

Every source format represents structure differently. If EPUB/Markdown/PDF logic leaks into translation or playback, complexity explodes. All parsers therefore terminate at `domain.Document`.

Scanned PDFs use a page producer that emits page-local `Document` values. Each
page is segmented by the same planner, then fed into the existing ordered
speech queue. Stable block IDs make streamed segment identities match the
complete PDF plan; full-document export and translated processing still build
the full `Document` before downstream work.

The pipeline only understands:

- ordered blocks;
- a hierarchy whose nodes map to block intervals;
- metadata and source provenance.

## Process boundaries

### Kokoro

Long-lived Python child process over JSONL stdin/stdout. Load model once.

### mpv

Long-lived child process controlled through JSON IPC. Append completed audio segments in sequence. Query position periodically for resume.

### local translators

Use a long-lived local inference process behind `translate.Translator`; do not
start a Python process for each segment. The Go adapter owns its lifecycle,
protocol, cancellation, cache interaction, and ordering.

### cloud translators

Direct provider APIs are first-class adapters. Agent CLIs such as Codex/OpenCode/Claude are optional runner adapters, not the primary mechanism.

## Concurrency

Each expensive stage can complete out of order internally. Before the next externally visible stage, a reorder gate commits only contiguous `Seq` values.

A bounded channel between stages provides backpressure. Never allow unbounded “translate the rest of the book” queues.

## Failure domains

- Parser failure: no processing starts; show actionable source error.
- One translation segment failure: retry policy, then pause pipeline; completed cache remains valid.
- TTS segment failure: retry, then pause; completed audio remains valid.
- mpv exits: generation may stop by default or continue to target buffer depending on session policy.
- process killed: atomic manifests/cache permit resume.

## No duplicate pipelines

Playback and export differ only at the sink. `--export` must reuse the exact same parse/translation/TTS/cache stages.
