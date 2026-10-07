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
  ocr/<cache-key>.json
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

Scanned-PDF OCR artifacts are cached one page at a time. Their identity
includes source content/document identity, parser and OCR format versions,
Tesseract version, language list, render resolution, and page number. Each
artifact is committed atomically only after Tesseract finishes; cancelling a
book leaves completed pages reusable. The PDF structure manifest is committed
after a full parse, so an interrupted live read starts recognition at its
first page while reusing completed page OCR and audio cache entries.

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
- PDF page and passage index within that page when reading a scanned PDF;
- offset within current audio file;
- playback speed;
- target language;
- voice;
- translation backend pinned per completed/active major unit;
- timestamp.

Write session state atomically and frequently enough that a hard kill loses little progress.

The CLI keys a session by the normalized source path and checkpoints the
current stable segment ID and sequence, major unit, section path, playback
offset, voice, speed, and document ID. PDF checkpoints also retain the physical
page and passage index on that page. Static plans save an initial checkpoint
and then poll mpv about once per second. Live scanned PDFs use a thread-safe
sequence resolver so the monitor only saves segments that the page producer
has actually emitted.

On restart, a scanned PDF with a saved position first traverses cached OCR pages
to rebuild the sequence mapping. The player remains open and responsive during
this restoration; Escape cancels the scan and returns home without replacing
the prior checkpoint. Playback then reads cached prefix pages but withholds
passages before the saved segment from synthesis and playback. New scanned-PDF
sessions still start speech as soon as the first page is ready. If PDF content
changed, playback selects the passage closest to the saved page and page-local
index, resets its offset, and reports the adjusted page. When the document ID
is unchanged but a path change altered segment IDs, page-local fallback keeps
the saved offset. A completed session starts from the beginning on the next
run.

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
- for scanned PDFs, map by stable segment ID, then saved page and page-local
  passage; reset offset when the document content ID changed;
- for other formats, map to the nearest structural location and reset offset.
