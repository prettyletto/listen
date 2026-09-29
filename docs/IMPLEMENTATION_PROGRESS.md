# Implementation progress

This records the user-visible scenario and verified exit criteria for each
vertical slice while the foundation is being implemented.

Every completed slice receives an independent, read-only reviewer subagent
checkpoint before work moves to the next slice. The reviewer checks the
implementation, tests/fixtures, docs, architecture constraints, and relevant
failure/privacy cases. Findings are resolved and re-reviewed before advancing.
After approval and required quality gates, commit that slice before beginning
the next one.

## Slice 0 — repository spine and invariants

**Scenario:** none; establish the command, domain, cache, error, and test seams
needed by later user-facing slices.

**Status:** complete. The command has a truthful help surface, storage paths
follow XDG, cache artifacts use atomic JSON writes, default errors have concise
messages and actionable hints, and fake speech/translation adapters are
available to pipeline tests.

**Verified:** `go test ./...`, `go vet ./...`, and `go run ./cmd/listen --help`.

**Reviewer:** Passed. The independent Slice 0 reviewer reported no findings in
the reviewed foundation files.

## Next: Slice 1 — `listen file.txt` streams via existing Kokoro

**Scenario:** `listen notes.txt` begins ordered playback after a small buffer,
while synthesis continues ahead of the listener.

**Exit criteria:** first audio starts before the complete source is synthesized;
rerunning reuses completed WAV segments; delayed fake TTS still commits audio in
source order; cancellation shuts down child processes and preserves completed
cache entries.

**Status:** Implementation and automated checks are complete; the independent
Slice 1 reviewer checkpoint is in progress. Work on Slice 2 waits for review
approval and resolution of any findings.

**Verified:** `gofmt`, `go test ./...`, `go vet ./...`, race tests for the
pipeline/speech/worker/playback packages, Python worker syntax parsing, and CLI
help output. Fake TTS verifies playback starts before the final segment and
that deliberately out-of-order completion still plays in source order. Speech
cache tests cover completed WAV reuse and malformed/truncated audio rejection.

**Environment note:** The real Kokoro path cannot be exercised in this
environment yet: Python 3.14 is installed and Kokoro is absent. Running a text
source reports `Run listen setup`; Slice 5 will provide the managed runtime.

**Reviewer:** Approved after follow-up. The reviewer found that idle mpv would
keep the command alive after the queue ended. `MPV.Wait` now detects the idle
state after all audio is queued, closes mpv on normal completion, and reports
early process exit. The focused playback tests pass.

## Next: Slice 2 — first-class EPUB structure

**Scenario:** `listen book.epub` understands parts, chapters, and sections,
while keeping spine reading order and beginning Chapter 1 quickly.

**Exit criteria:** EPUB fixtures cover package/spine order, XHTML semantic
blocks, nav/NCX hierarchy, fragment targets, outline intervals, semantic roles,
and TOC-page suppression, including split-file and multi-chapter-one-file
cases. Do not start Slice 3 until the parser fixtures, repository quality gates,
and independent Slice 2 reviewer pass.

**Status:** Complete after independent reviewer approval. EPUB parsing is
wired into the default source pipeline, with structure preserved in the
canonical Document IR.

**Verified:** full Document IR goldens cover EPUB 3 nested navigation, split
chapter files, multiple chapters in one XHTML file, EPUB 2 NCX fallback, and
missing-fragment repair. Generated cases also cover preserved non-linear
navigation and missing/unsupported targets. Focused tests cover direct
paragraph footnotes, TOC suppression, range invariants, major-unit promotion,
parser selection, and archive path safety. `go test ./...` and `go vet ./...`
pass.

**Runtime note:** Real audio playback still depends on the managed Kokoro
runtime delivered in Slice 5; the parser and segment planner are tested
independently here.

**Reviewer:** Approved after two follow-ups. The reviewer first found that
direct paragraph footnotes lost their `BlockNote` kind and that out-of-spine
navigation metadata was dropped. Those paths now have focused tests and
explicit IR representation. The follow-up then found that missing or
unsupported linear targets could be mislabeled auxiliary; collection now
requires a manifest resource outside the linear spine, with separate
diagnostics for missing and unreadable targets. The independent reviewer
approved after verifying all fixes.

## Slice 3 — Markdown + robust TXT structure

**Scenario:** `listen README.md` and text books preserve headings and chapters
without regex false positives.

**Exit criteria:** Markdown/TXT golden fixtures pass, parsers preserve source
semantics and stable ranges, and full repository quality gates pass. Slice 4
starts only after the independent Slice 3 reviewer approves.

**Status:** Complete. Implementation and automated exit criteria pass, and the
independent Slice 3 reviewer approved the follow-up fixes.

**Verified:** normalized Document goldens cover Markdown heading hierarchy,
front-matter metadata, paragraphs, lists, quotes, table rows, fenced/indented
code suppression, and plain-text chapter resets, TOC suppression, conservative
title inference, and source ranges. Shared role/major-unit thresholds are
covered at 80 words or three prose blocks. `go test ./...`, `go vet ./...`,
parser/app/segment race tests, and CLI help pass.

**Reviewer:** Approved after follow-up by an independent read-only reviewer.
The first review found three P2 edge cases: unlabeled TOCs, front-matter words
inside ordinary prose, and ambiguous chapter subtitles. TOC detection now
handles repeated chapter entries after a contents label and short dotted/page
runs without one. Front/back matter headings require standalone labels or an
explicit delimiter. A chapter subtitle now requires title-case/all-caps form
and a blank boundary after it. Dedicated regressions and an unlabeled-TOC
golden pass; the reviewer rechecked each finding and approved.

## Slice 4 — session resume, library cache, and export

**Scenario:** stop halfway, rerun `listen book.epub`, continue; `listen
book.epub --export` creates a reusable audiobook.

**Status:** Implementation and automated exit criteria pass; the independent
Slice 4 reviewer approved the follow-up. Slice 5 may begin.

**Verified:** versioned structure, segment, and session manifests are stored
atomically. mpv position polling saves stable segment IDs and offsets; the
forced-process-exit integration test reruns the app and verifies playback
begins at the saved segment and offset without starting synthesis again.
Export builds full MP3, M4B chapter metadata, chapter MP3s, playlist, and
manifest from the ordered pipeline results; the current pointer is replaced
only after the completed build is synced and committed. Export/cache tests
confirm completed audio is reused. `go test ./...`, `go vet ./...`,
`go test -race ./...`, and CLI help pass; ffmpeg and ffprobe verify the real
fixture output.

**Reviewer:** Approved after follow-up by an independent read-only reviewer.
The first review found that the forced-exit test stopped at session resolution
instead of restarting the app and proving playback resume. The app-level
subprocess test now persists a real session checkpoint, is killed, then reruns
the app and verifies the queued sequence and offset. A separate mpv load option
applies that offset during the initial file load, avoiding a seek race. The
reviewer confirmed the fix and reported no remaining P0–P2 findings.

## Slice 5 — setup, voices, config, doctor

**Scenario:** a Linux user installs local speech once, selects a listening
language and voice, and can inspect the runtime when something is missing.

**Status:** Complete after independent reviewer approval. The full runtime
dependency set is committed and setup enforces its package hashes.

**Verified so far:** setup provisions an XDG-private uv-managed Python 3.13
environment from a committed 91-package Linux x86_64 CPU lock with SHA-256
hashes and strict dependency checks. It runs a local synthesis health sample
and saves preferences only after success. `voices`, `config`, and `doctor` are
implemented. Fake installer/worker tests cover repeat setup, failure,
cancellation, voice selection, config override behavior, and missing runtime
diagnostics. The embedded worker no longer depends on a source checkout. The
lock passes `uv pip sync --dry-run --require-hashes --strict`. `go test ./...`,
`go vet ./...`, `go test -race ./...`, Python worker syntax parsing, and CLI
help pass. Manual `voices`, `config`, and `doctor` commands pass with isolated
XDG paths. A live model install and real synthesis were not run; setup behavior
is covered with fake installer and speech-worker implementations.

**Reviewer:** The initial read-only review found P2: transitive dependency
versions could vary because repeat setup used `uv pip install` from direct
requirements. Setup now embeds a complete generated lock and uses
`uv pip sync --require-hashes --strict`. The reviewer confirmed the fix and
approved Slice 5 with no remaining P0–P2 findings.

## Slice 6 — translation pipeline with a fake backend

**Scenario:** `--translate pt-BR` uses the same ordered segment stream after
translation, with cache reuse across interruption and resume.

**Exit criteria:** out-of-order fake translation and speech still commits in
source order, and interruption reuses both translation and speech caches.

**Status:** Complete. A fake-backend translation stage is wired into the
ordered stream. It batches only within one major unit and source locale,
validates complete outputs, reorders by sequence before speech, and persists
bounded context and translation artifacts. The CLI rejects remote translators
before reading a translated source. No production local translator is included
yet; that belongs to Slice 7.

**Verified so far:** unit coverage exercises out-of-order translation and
speech, target locale propagation into speech requests, cache identity across
text/locales/backend/context, translation and speech cache reuse after
interruption, remote-backend rejection, and target-locale audiobook
manifest/build identity. `go test ./...`, `go vet ./...`, `go test -race ./...`,
and CLI help pass.

**Reviewer:** Approved by an independent, read-only Slice 6 reviewer with no
remaining P0–P2 findings. The review confirmed translation validation and
segment matching, batch boundaries, ordered speech handoff, cache-backed
interruption recovery, locality checks before source reading and translation,
and target-language metadata in speech and audiobook export.

## Slice 7 — one local translator + realtime planner

**Status:** Implementation is in place. Full quality gates, the local worker
smoke-check, benchmark documentation, and independent review are complete.

**Verified:** the pinned Argos OPUS-MT English to Brazilian Portuguese package
installs from explicit setup, checks SHA-256, and commits atomically. Go owns a
single long-lived CPU CTranslate2 worker. The session preflights and caches up
to four opening translations, measures translation and speech stages, learns
target-locale duration from actual WAVs, and samples playback buffer health
once per second even during a producer stall. The benchmark and quality
limitations are documented. `gofmt`, `go test ./...`, `go vet ./...`,
`go test -race ./...`, Python worker syntax parsing, CLI help, the hash-locked
91-package setup dry run, and a real worker smoke check pass.

**Reviewer:** The first independent read-only review found P1: buffer health
was sampled only after audio append, so a stalled producer could miss the
45-second warning. The stream now samples on a cancellable one-second ticker;
the regression test reaches the warning threshold while synthesis is stalled
and verifies cancellation. The reviewer rechecked the fix and approved Slice 7
with no remaining P0–P2 findings.

## Slice 8 — explicit cloud fallback and provider adapters

**Status:** Complete after independent reviewer approval. Cloud translation is
behind `never|ask|allowed` policy, uses a direct OpenAI Responses adapter,
requires an explicit authorization wrapper before any request, and pins the
selected provider and model for the full operation. Glossary, style, and
bounded continuity context travel with each complete batch.

**Verified:** app integration tests cover `never`, declined and accepted `ask`,
`allowed`, local-to-cloud startup fallback, and one pinned provider across
multiple major units. Adapter tests cover structured complete responses,
non-storage requests, bounded errors, model discovery, cancellation,
credential handling, and rejection of raw or duplicate-ID requests.
`gofmt`, `go test ./...`, `go test -race ./...`, `go vet ./...`, CLI help, and
the missing-key model-discovery error path pass.

**Reviewer:** Approved after two follow-ups by an independent read-only
reviewer. The reviews found P2 gaps in the forgeable pipeline marker, duplicate
requested segment IDs, and exported cloud-authorization context creation. The
pipeline now checks only the private wrapper produced by `AuthorizeCloud`, the
OpenAI adapter rejects duplicate input IDs before a request, and context proof
creation is private. The reviewer confirmed all findings closed and reported
no remaining P0–P2 issues.

## Slice 9 — generic HTML

**Status:** Complete after independent reviewer approval.

**Scenario:** `listen article.html` reads the main article while suppressing
site navigation and footer content. The parser selects `<main>`, then a
dominant `<article>`, then falls back to body content with a diagnostic
warning. It preserves DOM headings, paragraphs, lists, quotes, tables, and
source DOM paths in the shared `Document` IR; EPUB continues to use its own
parser.

**Verified:** Golden fixtures cover all three selection paths, metadata,
semantic blocks, heading ranges, and navigation/footer/sidebar suppression.
An app-level `--no-play` test passes the selected content through segmentation,
cache, and fake synthesis. `go test ./...` and `go vet ./...` pass.

**Reviewer:** Approved by an independent read-only reviewer with no remaining
P0–P2 findings.

## Slice 10 — PDF best-effort

**Status:** Implemented and approved by an independent read-only reviewer;
automated exit criteria pass.

**Scenario:** text-based PDFs with reasonable structure become listenable;
scanned PDFs report that OCR is not available.

**Implemented:** Poppler extracts page-positioned text and image regions.
Repeated page furniture and margin page numbers are suppressed, columns are
read in column order, and headings are inferred from page typography. PDF
metadata is retained when present. QPDF bookmarks provide high-confidence
outline nodes when qpdf is installed; visual heading structure remains the
fallback. The parser stores a best-effort layout warning and identifies
scanned-only documents instead of returning an empty reading stream.

**Current limits:** tagged-PDF structure is reported but not consumed. OCR is
not implemented. PDF reading requires Poppler's `pdftohtml` and `pdfimages`
commands; qpdf is optional for bookmarks. Input is limited to 256 MiB and
5,000 pages; extractor output and each external tool runtime are bounded.

**Verified so far:** a three-page golden fixture covers two-column order,
bookmarks, repeated headers/footers, margin page numbers, and visual headings.
A scanned fixture verifies the `needs_ocr` error. An app-level `--no-play`
scenario sends reconstructed text through segmentation and fake synthesis.
Cancellation stops the extraction process and temporary raster output is
removed. `gofmt`, `go test ./...`, and `go vet ./...` pass. Manual CLI help
confirms the documented source list.

**Reviewer:** Approved by an independent read-only reviewer with no remaining
P0–P2 findings.

## Slice 11 — developer documents/directories

**Scenario:** `listen docs/` builds one ordered session from documentation
files and keeps each spoken segment within one source file.

**Status:** Complete after independent reviewer approval. Directory inputs
build one deterministic session from supported documentation files.

**Implemented:** Directory inputs recursively resolve Markdown, plain text,
HTML/XHTML, and PDF files. Applicable repository and nested `.gitignore` rules
control inclusion, symlinks and VCS metadata are skipped, and a documented
README/AGENTS/ADR/docs priority policy precedes deterministic path ordering.
Existing format parsers feed one composite Document IR with contiguous block
sequences, adjusted outline ranges, unique path-namespaced IDs, and preserved
source references. Directory file boundaries are honored during segmentation.
Directory structures are reparsed so changes to the file set or ignore rules
cannot return a stale structure manifest.

**Verified:** The directory golden covers metadata, source paths, ordered
blocks, ignore/negation behavior, and outline ranges. Tests verify stable IDs
and order across file creation order, inherited and nested ignore rules,
cancellation, directory cache invalidation, and `listen docs/ --no-play`
through fake synthesis. A shared-cache regression verifies that identical
relative directory names from different working directories retain their own
segment source paths. `go test ./...`, `go vet ./...`, and `git diff --check`
pass.

**Reviewer:** Approved after follow-up by an independent read-only reviewer.
The first review found a P2 segment-plan cache collision: identical relative
directory paths from different working directories could reuse stale absolute
source paths. The cache now keys directory plans by resolved absolute path, and
the reviewer confirmed the regression is closed with no remaining P0–P2
findings.
