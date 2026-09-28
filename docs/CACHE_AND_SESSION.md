# Cache, manifests, and resume

## Principles

- Cache stages independently.
- Writes are atomic.
- Never corrupt good work because a later stage failed.
- Re-running after interruption should continue near the failure point.

## Suggested XDG layout

```text
$XDG_CACHE_HOME/listen/
  structure/<manifest-key>.json
  segments/<manifest-key>.json
  translation/<cache-key>.json
  speech/<cache-key>.wav
  speech/<cache-key>.json
  worker/kokoro-worker-v1.py
  huggingface/...
  setup/health-check.wav   (removed after successful setup)
  sessions/<source-path-key>.json

$XDG_DATA_HOME/listen/
  runtime/python/...
  runtime/venv/...
  runtime/requirements.lock

$XDG_CONFIG_HOME/listen/config.json
```

## Cache-key layers

### Parsed structure

source content hash + parser/version + relevant parser options.

### Translation

source segment text hash + source/target language + backend/model + prompt
version + bounded context fingerprint (including glossary, style, summary,
and context-policy version).

### TTS

spoken text hash + engine/model version + voice + speed + speech-normalization version.

A change in translator should not invalidate identical TTS audio if the final spoken text is byte-identical.

Translated audiobook manifests record their target locale and use it in build
identity so translated and original-language exports do not share one build
pointer.

## Session state

Persist:

- document ID/path;
- active major unit;
- current segment ID/sequence;
- offset within current audio file;
- playback speed;
- target language;
- voice;
- translation backend pinned per completed/active major unit;
- timestamp.

Write session state atomically and frequently enough that a hard kill loses little progress.

The CLI keys a session by the normalized source path and checkpoints the
current stable segment ID and sequence, major unit, section path, playback
offset, voice, speed, and document ID. Playback saves an initial checkpoint and
then polls mpv about once per second. On restart it maps by stable segment ID;
if the source changed and that ID disappeared, it selects the nearest segment
in the same major unit or section and resets the offset to the start of that
segment. A completed session starts from the beginning on the next run.

Parsed structure manifests are keyed by source path, content hash, input
extension, and the parser/IR version. Segment plans are keyed by document ID,
source path, and segmentation options/version. The segment-plan version changes
when segment metadata changes, including source/target locale fields. Manifest
and session writes use atomic replacement.

## Source changes

If a source changes:

- recompute structure/segments;
- reuse stage artifacts whose content-derived keys still match;
- do not blindly resume by old sequence number;
- map resume to stable segment ID where possible, otherwise nearest structural location.
