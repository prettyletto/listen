# Performance model

## Primary latency budget

The product should optimize `command -> first audible sentence`.

Break it down explicitly:

```text
source open
+ structure parse/cache lookup
+ first segment planning
+ optional translation of startup batch
+ Kokoro synthesis of startup audio
+ mpv startup
= time to first audio
```

Do not wait for full-document parsing if a streaming parser can safely expose initial content, but correctness wins for formats such as EPUB where package/nav metadata is cheap and important.

## Throughput metrics

Record internally:

- parser MB/s and wall time;
- translation chars/tokens/s (diagnostic);
- translation playable-audio realtime ratio (decision metric);
- TTS playable-audio realtime ratio;
- ready-buffer seconds;
- buffer slope (seconds gained/lost per wall second);
- cache hit/miss counts;
- worker queue wait time.

## Buffer prediction

Let:

- `B` = ready audio seconds;
- `R` = producer realtime ratio for the current bottleneck;
- playback consumes 1 second/second.

Approximate buffer slope = `R - 1` while the pipeline is saturated.

If `R < 1`, estimated time to underrun is roughly:

`B / (1 - R)`

Use this only as one signal because stages are bursty and batching changes instantaneous rates.

## Local translator benchmark

Do not trust a setup-time synthetic benchmark alone. At translated playback
startup, measure up to four actual opening segments from the first major unit
for the selected language pair. Reuse the resulting translation-cache entries
in the stream, then update the session EMA with newly translated segments and
actual speech WAV durations. Store setup measurements only as prior
information; each session recalibrates against its own content.

## Memory

Bound queues. Large books must not cause the entire translated text or all decoded audio to remain in memory. Cached text/audio lives on disk; active pipeline memory should scale with buffer/window, not book length.
