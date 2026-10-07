# Implementation progress

This records the user-visible scenario and verified exit criteria for each
vertical slice.

Every completed slice receives an independent, read-only reviewer subagent
checkpoint before work moves to the next slice. The reviewer checks the
implementation, tests/fixtures, docs, architecture constraints, and relevant
failure/privacy cases. Findings are resolved and re-reviewed before advancing.
After approval and required quality gates, commit that slice before beginning
the next one.

## Interactive chapter player

Interactive Linux playback offers a start picker for the full document, an
unfinished saved position, or a playable chapter/major section. The player
screen shows the current location and readiness and handles pause/resume,
previous/next chapter, and stop. Chapter changes cancel the active stream and
restart from that boundary through the same ordered, cached pipeline.
Automated checks cover chapter grouping, keyboard/model behavior,
chapter-boundary restart, mpv controls, and player process lifetime.

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

## Slice 1 — `listen file.txt` streams via existing Kokoro

**Scenario:** `listen notes.txt` begins ordered playback after a small buffer,
while synthesis continues ahead of the listener.

**Exit criteria:** first audio starts before the complete source is synthesized;
rerunning reuses completed WAV segments; delayed fake TTS still commits audio in
source order; cancellation shuts down child processes and preserves completed
cache entries.

**Status:** Complete after independent reviewer approval. Ordered playback,
speech caching, resume, cancellation, and the Kokoro worker are wired through
the user-facing command.

**Verified:** `gofmt`, `go test ./...`, `go vet ./...`, race tests for the
pipeline/speech/worker/playback packages, Python worker syntax parsing, and CLI
help output. Fake TTS verifies playback starts before the final segment and
that deliberately out-of-order completion still plays in source order. Speech
cache tests cover completed WAV reuse and malformed/truncated audio rejection.

**Runtime verification:** The initial Slice 1 checkpoint used fake synthesis
because the host Python was 3.14 and Kokoro was absent. After Slice 5 added the
managed runtime, an isolated setup and a one-segment `--no-play` run passed on
2026-09-29 with CPython 3.13.15 and Kokoro 0.9.4.

**Reviewer:** Approved after follow-up. The reviewer found that idle mpv would
keep the command alive after the queue ended. `MPV.Wait` now detects the idle
state after all audio is queued, closes mpv on normal completion, and reports
early process exit. The focused playback tests pass.

## Slice 2 — first-class EPUB structure

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

**Scenario:** a Linux user installs local speech, selects listening languages
and a voice for each, and can inspect the runtime when something is missing.

**Status:** Complete after independent reviewer approval. The full runtime
dependency set is committed and setup enforces its package hashes.

**Verified so far:** setup provisions an XDG-private uv-managed Python 3.13
environment from a committed 91-package Linux x86_64 CPU lock with SHA-256
hashes and strict dependency checks. It runs a local synthesis health sample
and saves preferences only after success. `voices`, `config`, and `doctor` are
implemented. The interactive setup wizard selects multiple locales, a default
voice per locale, and optionally the local translator; it runs again for
interactive setup requests while non-interactive setup uses saved/default
choices. Fake installer/worker tests cover repeat setup, failure, cancellation,
voice selection, config override behavior, and missing runtime diagnostics. The
embedded worker no longer depends on a source checkout. The
lock passes `uv pip sync --dry-run --require-hashes --strict`. `go test ./...`,
`go vet ./...`, `go test -race ./...`, Python worker syntax parsing, and CLI
help pass. Manual `voices`, `config`, and `doctor` commands pass with isolated
XDG paths. An isolated live setup then installed the managed CPython 3.13.15
runtime and locked Kokoro 0.9.4 dependencies, completed the real synthesis
health check, and generated one source segment through `listen --no-play` on
2026-09-29. The setup used temporary XDG directories; no user runtime or cache
was modified. The first real provision attempt exposed a conflict between
`UV_PYTHON_PREFERENCE=only-managed` and `uv venv --managed-python`; setup now
uses the explicit managed-Python flag without the conflicting environment
setting, with a regression assertion and a successful live rerun. `uv` still
warns that Kokoro/Misaki package metadata declares Python below 3.13; the
current locked install, health synthesis, and source smoke test pass, but that
upstream compatibility declaration should be revisited during dependency
updates.

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

**Scenario (Slice 10 baseline):** born-digital PDFs with reasonable structure
become listenable; scan-like pages were diagnosed rather than misread.

**Implemented:** Poppler extracts page-positioned text and image regions.
Repeated page furniture and margin page numbers are suppressed, columns are
read in column order, and headings are inferred from page typography. PDF
metadata is retained when present. QPDF bookmarks provide high-confidence
outline nodes when qpdf is installed; visual heading structure remains the
fallback. The parser stores a best-effort layout warning and identifies
scanned-only documents instead of returning an empty reading stream.

**Current limits at this slice:** tagged-PDF structure was reported but not
consumed. OCR support was added later in Slice 13. PDF reading requires
Poppler's `pdftohtml` and `pdfimages` commands; qpdf is optional for bookmarks.
Input is limited to 256 MiB and 5,000 pages; extractor output and each external
tool runtime are bounded.

**Verified so far:** a three-page golden fixture covers two-column order,
bookmarks, repeated headers/footers, margin page numbers, and visual headings.
A scanned fixture at that time verified the `needs_ocr` diagnostic. An
app-level `--no-play` scenario sends reconstructed text through segmentation
and fake synthesis.
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

## Slice 12 — Go AST code narration

**Scenario:** `listen main.go --code` narrates package structure, declarations,
comments, signatures, and recognized control flow through the reader pipeline.

**Status:** Complete after independent reviewer approval. Go narration remains
opt-in, and code-mode structure manifests have their own parser-versioned
identity.

**Implemented:** `go/parser` and AST nodes produce spoken package/import
overviews, type and function signatures, doc comments, and concise control-flow
summaries. Method section titles include receiver types and stable outline IDs
include raw receiver identities. Source reads are capped at 8 MiB and parsed
documents at 250,000 AST nodes; context cancellation is checked during AST
validation and nested body narration. Narrated blocks use the existing
segmentation, cache, speech, playback, and export path.

**Verified:** A golden fixture covers package/import summaries, types,
interfaces, signatures, comments, control flow, and same-named methods on
different receiver types. Tests cover opt-in parser selection, shared pipeline
use, separate cache identity, receiver-unique outline IDs, cancellation,
resource limits, and actionable CLI errors. `go test ./...`, `go vet ./...`,
CLI help, and `git diff --check` pass.

**Reviewer:** Approved after follow-up by an independent read-only reviewer.
The first review found duplicate method outline IDs and inadequate cancellation
checks during body narration. Receiver-aware identities, bounded input/AST
work, and context checks resolve both findings. A final cache-path review
confirmed unsupported extensions are rejected before code-mode size checks or
fingerprinting; no P0–P2 findings remain.

**Current limit:** Numeric literals retain their Go source spelling, so
hexadecimal or underscored values may not sound as naturally as decimal values.

## Slice 13 — local OCR and streamed scanned-PDF playback

**Scenario:** `listen scanned.pdf` recognizes scan-like pages locally, begins
playback before OCR reaches the end of the document, and reuses completed OCR
and speech cache artifacts after interruption.

**Status:** Complete after independent reviewer approval. Local OCR, page
streaming, ordered speech generation, cache reuse, and progress are wired
through the user-facing reader.

**Implemented:** Poppler preflight identifies candidate pages and Tesseract
converts local TSV word geometry into the existing PDF layout and Document IR.
Page results flow through the existing segment planner and ordered speech/cache
pipeline. The normal playback path caps its opening buffer at three seconds;
it starts sooner when the first page ends with a shorter ready audio buffer.
`--no-play`, `--export`, and translated reading complete OCR first and show
page progress. Config defaults to English and accepts installed Tesseract IDs
such as `eng+por`. OCR is local, content-addressed, and atomically cached per
page. Low-confidence and empty OCR pages are reported. Chapter navigation is
unavailable while OCR is still discovering the complete PDF outline. Slice 15
adds streamed-PDF passage checkpoints and restoration.

**Verified:** a two-page image-only PDF golden checks English OCR text
and source-page order. Parser coverage verifies page-local IR ranges, identical
segment IDs between streaming and complete plans, and interruption after page
one followed by cache reuse on resume. Pipeline and app integration tests use
opening audio shorter than the startup threshold, delay page two OCR, and
verify the opening audio is queued before page two finishes. An app-level
`--no-play` test verifies progress, OCR warnings, and text reaching fake speech.
Settings and parser tests cover language configuration and validation. The only
committed OCR quality fixture currently covers English. The full `go test
./...` and `go vet ./...` gates pass, as do race tests for the app, pipeline,
and PDF parser, `git diff --check`, and `listen --help` output.

**Reviewer:** Approved after follow-up by an independent read-only reviewer.
The first review found that short opening pages could keep playback below its
startup threshold until later OCR completed. The stream now flushes at the
first nonempty page boundary once that page's ordered audio is ready. The
reviewer confirmed the regression is covered and reported no remaining P0–P2
findings.

## Slice 14 — personal listening home and saved return loop

**Scenario:** `listen` opens a local home; choose or add a source, listen, press
Escape to save and return, then continue it from the home.

**Status:** Complete. Independent reviewer approved after fixes for stale home
state after direct Escape, catalog-save feedback, and saving-progress timing.

**Implemented so far:** Added a versioned local source catalog in XDG data
storage. It preserves cleaned absolute-path identity, serializes concurrent
mutations across processes, writes atomically, and returns removed entries for
undo without touching source or cache files. Added a home view with recent
sources, source-path entry, search, and a shared Bubble Tea session for home
and playback. Interactive direct-source commands still start the supplied
source immediately; Escape now returns home and q/Ctrl-C exits. The default
start-point picker has been removed so playback resumes or begins immediately.

**Verification:** `go test ./...`, `go vet ./...`, and targeted race tests
passed. An independent read-only review confirmed the home/play/home loop and
found no remaining Critical or Major findings after the fixes above. Home
currently accepts a typed or pasted path; guided setup and preferences are a
later slice.

## Slice 15 — resumable streamed scanned PDFs

**Scenario:** stop during a scanned PDF, rerun it, and resume at the saved
passage without replaying earlier audio.

**Status:** Complete. Independent read-only reviewer approved with no
unresolved Critical or Major findings.

**Implemented:** Checkpoints now include the stable passage ID and sequence,
physical PDF page, page-local passage slot, and audio offset. A synchronized
resolver lets the playback monitor checkpoint actual streamed segments rather
than the former placeholder. A returning read shows cancellable OCR-cache
restoration progress in the player, rebuilds the sequence through the saved
passage, then skips prefix segments before they reach speech or playback. New
reads still stream their first page immediately. Changed PDF content falls
back to the nearest passage on the saved page or nearest non-empty page, resets
the offset, and names the adjusted page. Same-content path changes keep the
offset when the stable ID changes with the path.

**Verification:** Added an app-level scanned-fixture stop/restart test that
saves a mid-passage page-two position, resumes at its offset, and asserts the
page-one sequence is never appended on the second run. A subprocess test kills
a process after a dynamic PDF checkpoint and verifies that page, passage slot,
and offset remain recoverable. Unit tests cover thread-safe streamed checkpoint
mapping, same-page and nearest-page fallback, same-content offset preservation,
and Escape cancellation during restoration. The player visibly keeps the
Escape cancel and q exit controls during restoration at 40×15, 80×24, and
120×30. `go test ./...`, `go vet ./...`, `git diff --check`, and targeted race
tests passed. The independent reviewer reran the focused app, session, and UI
tests with the writable workspace temp directory and approved the slice with no
Critical or Major findings.

## Slice 16 — source browsing and in-player navigation

**Scenario:** browse to a source from the listening home, recover moved files,
and select chapters without losing the playback state.

**Status:** Complete. Independent read-only reviewer approved after follow-up fixes for streamed scanned-PDF chapter controls and relink save-failure recovery.

**Implemented:** The home now focuses the latest unfinished source as its
one-Enter Continue action and labels recent sources as Continue, Start
listening, or Start again. The filesystem browser starts at the current
directory, filters hidden files by default, supports path search and parent
navigation, and lets users select the current folder as documentation. Missing
entries offer Locate, Remove, Back, and Undo; relinking keeps the existing
destination checkpoint when present, transfers an old checkpoint only when
reading options match, and lets static/PDF resume resolution reset offsets when
content changed. The player adds searchable chapter navigation, restores the
prior paused/playing state after cancellation, and displays resolver-backed PDF
pages with passage-local timing labels. The player model declares chapter
navigation availability and its reason: streamed scanned PDFs hide chapter
shortcuts and explain that recognition continues when those keys are invoked
or the user opens help. Multi-chapter sources retain their chapter controls.

**Verification:** `go test ./...`, `go vet ./...`, and targeted race tests for
playerui, app, session, and catalog pass. Rendered checks at 40×15, 80×24, and
120×30 cover home, browser, missing-source, help, loading/restoration/playing/
paused/error/completion player states, page visibility, minimum-size guidance,
and no-color output. Chapter tests cover cancellation from both paused and
playing states plus selection at the chapter boundary. Relink tests verify
that the old entry survives a failed replacement save and is removed only
after successful upsert. The independent read-only reviewer approved after
confirming the scanned-PDF chapter controls remain honest, then approved the
catalog-failure recovery follow-up. Full tests, vet, targeted race checks,
formatting, and `git diff --check` pass.

## Slice 17 — guided setup and listening preferences

**Scenario:** select a source first, explicitly start cancellable local setup,
then listen with saved voice and speed preferences.

**Status:** Complete. The independent reviewer approved the source-validation,
responsive setup/preferences, and navigation-exit follow-ups with no unresolved
findings.

**Implemented:** The home offers source-first setup with an explicit start
action, cancellable installation and health-check progress, retry after
cancellation or failure, and automatic start/resume of the retained source
after success. Selected files and documentation folders are checked before
the setup offer, so a missing, unreadable, empty, or unsupported source does
not trigger a local download; the add-path form stays open with the path and
an actionable error so it can be corrected. The interactive path uses the
non-interactive provisioner so it does not compete with Bubble Tea; standalone
`listen setup` retains its guided CLI wizard. Home preferences edit the default
voice, per-language voice mappings, speech-generation speed, and playback
speed. A fixed local voice sample runs through cancellable speech and playback
workers, and its temporary audio is removed on exit. Playback speed can change immediately with `+` and `-`; voice
and speech speed apply next session. Version-1 settings migrate to playback
rate 1×. The `playback-speed` config setting and `--playback-speed` override
preserve existing CLI behavior. Voice selection does not enable translation.

**Focused verification:** Tests cover explicit setup start, cancellation and
retry, retaining the selected source across setup/preferences overlays,
preferences save/discard and `q` exit, v1 rate migration, preference persistence
without changing cloud consent, preview cancellation and process/temp-file
cleanup, live playback-rate persistence, CLI rate overrides, and setup,
preferences, and preview rendering at 40×15, 80×24, and 120×30. Tests also
verify missing, empty, unreadable, and unsupported files and folders are
rejected before setup readiness is queried. Setup choice, retry/error,
progress, preferences, and preview fit all three sizes; setup and preference
screens plus preview output remain free of ANSI color codes when color is
disabled. The full repository `go test`, `go vet`, and targeted race gates pass;
an 80×24 terminal smoke test confirmed the empty home opens and `q` exits cleanly.
The independent reviewer approved the final implementation with no Critical,
Major, or Moderate findings.
