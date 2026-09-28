# AGENTS.md — implementation contract

This repository is deliberately over-specified so an implementation agent can work slice-by-slice without inventing product behavior.

## Product invariant

The user-facing product is a reader, not an AI workbench. The normal command is:

```sh
listen <source>
```

Do not surface model names, chunk sizes, worker counts, ONNX details, cache keys, or provider internals in the default flow.

## Implementation discipline

- Implement the vertical slices in `docs/VERTICAL_SLICES.md` in order.
- Keep the repository compiling and tests green after every slice.
- Prefer small packages with narrow interfaces. No god files.
- Keep domain types free of provider-specific fields.
- Do not make Python the orchestration layer. Python is only an inference worker unless an ADR explicitly changes this.
- Do not spawn Python once per segment. Keep the Kokoro worker alive.
- Do not stream partial LLM tokens into TTS. Translation commits at segment boundaries.
- Never permit out-of-order playback. Every concurrent stage must re-order before committing downstream.
- Never upload user content without explicit prior cloud permission.
- Never make EPUB chapter detection regex-first. Use package/spine/navigation semantics.
- PDF is best-effort and must report degraded structure rather than pretending confidence.
- Avoid adding dependencies until the slice that needs them.

## Quality gates

Every parser must have golden fixtures. Every concurrent stage must have deterministic ordering tests. Every cache write must be atomic. Every external process must be cancellable through `context.Context` and cleaned up on shutdown.

Use fake translation/TTS implementations in normal CI. Real model tests are opt-in integration tests.

## Definition of done for a slice

A slice is not complete until:

- its user-visible scenario works end-to-end;
- unit and integration tests cover success, cancellation, interruption, and resume where relevant;
- docs match behavior;
- errors are actionable but do not expose implementation noise by default;
- `go test ./...` passes;
- `go vet ./...` passes.

## Architecture guardrails

The canonical flow is:

`Source -> Document IR -> Segment Plan -> optional Translation -> ordered commit -> TTS -> ordered audio queue -> cache -> playback/export`

The same source/segment/cache objects feed both playback and export. Do not implement a separate export generation path.
