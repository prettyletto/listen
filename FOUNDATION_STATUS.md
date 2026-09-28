# Foundation status

This ZIP intentionally contains a **compiling skeleton plus normative design**, not a fake half-implementation.

Already decided:

- Go product core, Python Kokoro worker.
- canonical document IR.
- EPUB spine/nav/NCX semantics and heading enrichment.
- semantic segments and stable ordering.
- same pipeline for streaming/export.
- local realtime viability planner.
- explicit cloud consent.
- vertical implementation order and acceptance gates.
- synthetic EPUB/Markdown/TXT parser fixtures.

Still implementation work by design:

- concrete CLI parser/UI;
- XDG/cache/session implementation;
- Kokoro worker wiring from prototype;
- mpv IPC;
- source parsers;
- translation backends;
- setup/packaging.

Start with `docs/LUNA_START_PROMPT.md`.
