# Semantic segmentation

Segments are the unit of translation, TTS caching, ordering, buffering, and resume. They must be stable and meaningful.

## Do not segment by raw character count first

Start from parsed blocks and structural boundaries.

Priority:

1. Never cross a major-unit/chapter boundary.
2. Prefer one or more complete paragraphs.
3. Keep headings attached to the prose they introduce when latency allows.
4. Split long paragraphs at sentence boundaries.
5. Only split inside a sentence as a last-resort safety limit.

## Target duration, not just tokens

Streaming UX cares about audio time. Initial targets:

- ideal segment: ~15–35 seconds of speech;
- hard maximum: ~60 seconds;
- very short headings may be combined with following prose;
- section/chapter boundaries always force a flush.

Estimate duration from language-aware words/minute until actual Kokoro duration is known. Calibrate estimates during a session.

## Translation batching vs segment identity

Segment identity remains fixed, but translators may batch several segments in one request for throughput.

Each planned segment carries its source locale when the document provides one.
After translation commits, the pipeline sets `TargetLang` and `SpokenText` on
that same segment before speech caching or export; `SourceText` remains intact.

A translation batch must preserve IDs:

```json
[
  {"id":"s41","text":"..."},
  {"id":"s42","text":"..."}
]
```

The adapter returns one complete translation per ID. Partial model tokens never become committed segments.

## Stable IDs

A stable segment ID should derive from:

- document identity;
- source locator/structural path;
- normalized source text hash;
- segmentation algorithm version.

Cache keys additionally include transformation settings.

Avoid using only sequence number: inserting a paragraph near the beginning should not invalidate every later cached segment unnecessarily.

## Spoken normalization

Keep three conceptual texts distinct:

- `SourceText`: exact normalized content from parser.
- translated text: optional target-language result.
- `SpokenText`: final TTS-oriented normalization.

Examples of spoken normalization:

- strip URLs while retaining link labels;
- pronounce abbreviations/code identifiers intelligently;
- convert visual table delimiters into natural pauses;
- omit note markers;
- code mode transforms AST/syntax into narration.

Do not mutate source text in place.
