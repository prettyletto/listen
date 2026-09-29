# Vertical slices

Each slice ends in something the user can run. Do not parallelize slices that change the same core contracts.

## Independent review gate for every slice

After a slice meets its user-facing scenario and automated exit criteria, pause
before starting the next slice and request an independent, read-only reviewer
subagent. The reviewer inspects the completed slice's implementation, tests,
fixtures, and docs against its scenario, the repository's architecture rules,
failure/cancellation behavior, and applicable privacy/security requirements.
The reviewer reports concrete findings with severity and file/line references;
it does not edit implementation files. Resolve every finding and request a
follow-up review before advancing. Record the review outcome and any resolved
findings in `docs/IMPLEMENTATION_PROGRESS.md`.

| Slice | Review focus |
| --- | --- |
| 0 | Domain seams, XDG paths, atomic writes, errors, CLI truthfulness, reusable fakes |
| 1 | Plain-text structure, stable segments, ordered streaming, cache integrity, worker lifecycle, mpv cancellation |
| 2 | EPUB package/spine authority, nav/NCX hierarchy, fragment intervals, TOC suppression, corpus coverage |
| 3 | Markdown AST semantics, plain-text heading confidence, false-positive resistance, shared unit classification |
| 4 | Manifest atomicity, stable resume mapping, forced interruption, shared playback/export cache path |
| 5 | Managed runtime safety, pinned dependencies, setup idempotence, voice/config/doctor behavior |
| 6 | Translation cache identity, batching/order, glossary context, cancellation and resume |
| 7 | Benchmark validity, viability thresholds, buffer predictions, sustainable/unsustainable classification |
| 8 | Consent policy enforcement, provider boundaries, credential handling, no-upload tests |
| 9 | Main-content extraction, semantic block preservation, nav/footer suppression, EPUB regression safety |
| 10 | Geometry/order reconstruction, confidence reporting, scanned-PDF honesty, corpus coverage |
| 11 | Ignore rules, deterministic ordering, document composition, source traceability |
| 12 | Go AST narration quality, spoken signatures/comments, `--code` opt-in behavior |

Slice exit criteria include reviewer approval. A slice with unresolved blocking
findings is not complete, even if its automated checks pass.

## Slice 0 — repository spine and invariants

**User value:** none; engineering foundation.

Implement:

- CLI command skeleton;
- domain document/segment interfaces;
- reorder buffer;
- XDG path helpers;
- atomic JSON/file helpers;
- structured errors and basic terminal output;
- fake TTS/translator test doubles.

Tests: reorder, atomic writes, cancellation helpers.

Exit: `go test ./...` and `go vet ./...` green.

---

## Slice 1 — `listen file.txt` streams via existing Kokoro

**Scenario:** `listen notes.txt` begins playback after a small buffer instead of generating the whole file.

Implement:

- UTF-8/plain-text parser -> blocks;
- semantic paragraph/sentence segmenter;
- long-lived Python Kokoro worker using prototype audio logic;
- Go worker client/protocol;
- speech cache;
- mpv JSON IPC player;
- ordered audio buffer;
- graceful cancellation.

No translation. No EPUB yet.

Exit:

- first audio begins before whole file completes;
- kill/re-run reuses completed WAVs;
- deliberately delayed fake TTS still plays strictly in order.

---

## Slice 2 — first-class EPUB structure

**Scenario:** `listen book.epub` correctly understands parts, chapters, sections and starts reading Chapter 1 quickly.

Implement exactly `BOOK_STRUCTURE_AND_PARSING.md` EPUB algorithm:

- OCF/package/spine;
- XHTML semantic blocks;
- nav/NCX hierarchy;
- fragment target index;
- outline intervals;
- semantic roles;
- major-unit classifier;
- TOC-page suppression;
- golden corpus.

Exit: all EPUB parser fixtures pass, including split-file and multi-chapter-one-file cases.

---

## Slice 3 — Markdown + robust TXT structure

**Scenario:** `listen README.md` and text books preserve headings/chapters without regex false positives.

Implement:

- Goldmark AST parser;
- Markdown block semantics/code skipping;
- plain-text scored heading inference;
- probable TOC-region detection;
- shared major-unit classifier.

Exit: golden Markdown/TXT fixtures pass.

---

## Slice 4 — session resume, library cache, and export

**Scenario:** stop halfway, rerun `listen book.epub`, continue; `listen book.epub --export` creates a reusable audiobook.

Implement:

- structure/segment/session manifests;
- mpv position polling and atomic session checkpoints;
- stable-segment resume mapping;
- export sink using the same ordered pipeline and cached audio segments;
- full-book MP3 and chapter-aware M4B, individual chapter MP3s, and playlist
  assembly through ffmpeg;
- atomic `current.json` build pointer inspired by the Python prototype.

Exit: a forced-kill integration test resumes from a persisted segment and
offset; export does not synthesize already-cached audio again and never points
`current.json` at a partial build.

---

## Slice 5 — `listen setup`, voices, doctor

**Scenario:** a new Linux user can install once and then use `listen book.epub` without Python environment knowledge.

Implement:

- managed Kokoro Python environment;
- language/voice selection;
- selective voice/model download where upstream allows;
- health synthesis;
- config file;
- `listen voices`, `listen config`, `listen doctor`.

Exit: deleting managed runtime then running setup recreates a healthy environment idempotently.

---

## Slice 6 — translation pipeline with a fake backend

**Scenario:** architecture supports `--translate pt-BR` before any real provider is trusted.

Implement:

- translator interface;
- translation cache;
- batching;
- ordered translation commit;
- glossary/context artifact;
- target locale on segments;
- fake translator that sleeps/completes out of order.

Exit: out-of-order translation + TTS still plays in source order; interruption resumes both caches.

---

## Slice 7 — one local translator + realtime planner

**Scenario:** `listen book.epub --translate pt-BR` uses local translation when it can sustain playback and warns when it cannot.

Implement:

- one local backend chosen after real benchmark (TranslateGemma-class is a candidate, not dogma);
- setup/install path;
- translation throughput measurement;
- spoken-duration estimation + actual-duration calibration;
- EMA ratios;
- startup viability test;
- buffer health prediction;
- `auto` planner.

Exit: simulated slow backend is classified unsustainable; export mode still permits it.

---

## Slice 8 — explicit cloud fallback and provider adapters

**Scenario:** when local translation cannot keep up, configured cloud fallback can take over with explicit consent/policy.

Implement:

- cloud policy `never|ask|allowed`;
- at least one direct provider adapter;
- model discovery/config surface;
- provider pinning by major unit;
- glossary/style continuity across boundary;
- no silent upload tests.

Then add OpenAI/Gemini/Anthropic adapters behind identical contracts as desired.

Exit: integration tests prove remote adapter is never invoked under `never`, prompts under `ask`, and may auto-switch only under `allowed`.

---

## Slice 9 — generic HTML

**Scenario:** `listen article.html` reads main article content without site chrome.

Implement main/article extraction, DOM headings, semantic blocks, tables/quotes.

Exit: fixtures demonstrate nav/footer suppression without damaging EPUB parser behavior.

---

## Slice 10 — PDF best-effort

**Scenario:** born-digital PDFs with reasonable structure become listenable; scanned PDFs fail honestly.

Implement:

- text+geometry extraction;
- bookmark ingestion;
- repeated header/footer/page-number suppression;
- column reconstruction;
- visual heading classifier;
- scanned-document detection;
- low-confidence warnings.

Exit: PDF golden corpus passes. Do not claim OCR support yet.

---

## Slice 11 — developer documents/directories

**Scenario:** `listen docs/` forms an ordered session from documentation files.

Implement:

- directory source resolver;
- ignore rules (`.gitignore` where appropriate);
- deterministic file ordering;
- README/AGENTS/docs/ADR prioritization policy;
- composite Document IR.

Exit: repository documentation fixture yields stable outline/session order.

---

## Slice 12 — code narration

**Scenario:** `listen main.go --code` narrates semantics instead of punctuation soup.

Start with Go only using `go/parser`/AST:

- package/import overview;
- type/function signatures in spoken form;
- doc comments;
- concise body-level control-flow narration.

Do not make source code first-class until book/document experience is solid.

Exit: the Go golden fixture demonstrates spoken package/import summaries,
type and function signatures, doc comments, and control-flow narration. The
CLI flag selects this parser only when requested, and generated speech uses
the shared reader pipeline and cache.
