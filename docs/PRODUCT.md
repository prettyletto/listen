# Product definition

## One sentence

`listen` makes anything readable immediately listenable.

It is not primarily an audiobook generator, model manager, TTS demo, or AI workflow UI. Audiobook export, translation, code narration, and provider selection are capabilities behind a reader.

## Target users

### Primary: readers

People with EPUBs, articles, PDFs, notes, and text files who want to hear them without converting/importing/configuring a project first.

### Secondary: developers

People who want to listen to README files, design docs, ADRs, specifications, repository documentation, and eventually source code in a narration designed for comprehension rather than literal punctuation.

## Product hierarchy

1. **Immediate reliable playback**
2. Correct document/book structure
3. Resume and caching
4. Local privacy and offline operation
5. Translation
6. Export
7. Advanced model/provider control
8. Developer-specific code narration

This order matters. Do not implement a sophisticated translation UI before `listen book.epub` starts quickly and resumes reliably.

## Public mental model

The user should learn at most these ideas:

- `listen setup` — prepare the machine once.
- `listen <thing>` — listen now.
- `--translate <language>` — hear it in another language.
- `--export` — produce files instead of starting playback.

Everything else is discoverable via `listen --help`, `listen voices`, `listen models`, `listen config`, `listen doctor`, and `listen inspect`.

## Success metrics

Measure product performance with:

- time from command to first audible sentence;
- percentage of sessions that start without a user decision;
- playback underrun count;
- resume correctness after forced termination;
- chapter/section boundary accuracy on the golden corpus;
- local translation sustainable realtime ratio;
- cache hit rate on repeated sessions.

Total full-book generation time is a secondary metric in streaming mode.

## Non-goals for v1

- voice acting/casting studio;
- timeline editor;
- dubbing/video/subtitles;
- pronunciation editor UI;
- OCR as a hard dependency;
- cloud account management dashboard;
- DRM circumvention;
- “support every file format” marketing claims.
