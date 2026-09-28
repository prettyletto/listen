# Kokoro worker

Keep Kokoro in Python initially because the prototype is proven and the ecosystem is mature. Keep everything around it in Go.

## Lifecycle

1. Go resolves the private CPython 3.13 worker environment managed by `listen setup`.
2. Start `python -m ...worker` once.
3. Worker imports/loads Kokoro and chosen language pipeline.
4. Worker emits `ready`.
5. Go sends synthesize requests.
6. Worker creates atomic WAV files and returns duration/path.
7. Go validates/cache-indexes the result.
8. Shutdown gracefully or kill on context deadline.

## Voice/language loading

`listen setup` downloads only requested language/voice assets where the Kokoro packaging permits it. If upstream Kokoro packages a shared model plus separate voices, keep the shared model once and voice files per configured language.

The worker may lazily create one language pipeline at a time and cache pipelines in-process. Do not make the Go core aware of Kokoro `lang_code` details beyond a voice capability registry.

The worker script is embedded in the Go binary and atomically written under
the XDG cache when the process starts. Python, Kokoro package files, model
weights, and voice data remain in the private runtime or XDG cache rather than
depending on a source checkout.

## Protocol safety

stdout is JSONL only. stderr is logs. A malformed line is a worker protocol error, not user text.

Validate output path: worker may write only under a Go-created cache/temp directory passed in the request.

## Future replacement

A native Go/ONNX synthesizer may later implement the same `speech.Synthesizer` interface. The rest of the product must not care.
