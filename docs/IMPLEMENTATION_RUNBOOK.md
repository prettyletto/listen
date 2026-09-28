# Implementation runbook for Luna/agent

## Before editing

Read all required docs from `README.md`. Inspect `reference/python-prototype/` to understand existing behavior worth preserving, especially chunk caching, spoken Markdown normalization, book-like detection, and output/playback UX. Do not port its regex-heavy structure parser wholesale.

## Working method

For each vertical slice:

1. Restate the slice's user-visible scenario in a short implementation note.
2. Add/extend domain interfaces only as required.
3. Add fixtures/tests first for structural behavior.
4. Implement the minimum vertical path.
5. Run `gofmt`, `go test ./...`, `go vet ./...`.
6. Run the slice's manual acceptance command.
7. Update docs if actual behavior differs.
8. Commit only after exit criteria pass.

Do not create giant future abstractions to “prepare” for later slices.

## First architectural checkpoint

At the end of Slice 2, the following must already be true:

- Go parses and segments sources.
- Python only synthesizes text it is handed.
- Go controls mpv.
- generation can run ahead of playback;
- cache survives restart;
- segment ordering is deterministic.

If not, fix architecture before adding translation.

## Parsing checkpoint

Before translation work begins, EPUB golden fixtures must demonstrate:

- nested TOC preserved;
- fragment-targeted subsections mapped;
- chapters spanning files handled;
- multiple chapters within one file handled;
- NCX fallback;
- TOC page skipped by default;
- front/back matter roles;
- broken fragment repair warning.

Translation built on incorrect chapter boundaries will create harder bugs later.

## Translation checkpoint

Start with a deterministic fake translator. Prove batching, caching, ordering, cancellation, and provider pinning before integrating a real model.

## Performance checkpoint

Measure:

- parse time;
- time to first segment;
- time to first 30s of audio;
- TTS realtime ratio;
- translation realtime ratio;
- memory high-water mark.

Optimize only after measurements identify the bottleneck.
