# Streaming, ordering, backpressure, and local viability

## Goal

Playback should begin once enough **ordered completed audio** exists, while later stages continue ahead of the listener.

## Channels

Conceptual bounded queues:

```text
segments        capacity ~16
translation work capacity ~8 batches
translated      capacity ~16
speech work     capacity ~8
ready audio     duration-bounded, not just item-bounded
```

Exact capacities are tuned by benchmarks. They are not public UX knobs.

Scanned PDFs use a bounded page producer before the segment queue. Poppler
preflights layout and scan-like pages, then Tesseract recognizes one candidate
page at a time in source order. Each page's segments enter the same bounded,
ordered speech queue as other sources. The PDF path caps its opening buffer at
three seconds and flushes ready audio at the first completed page boundary, so
a short opening page does not wait for later OCR.
Synthesis and OCR overlap; page results never overtake earlier pages. A
producer error cancels downstream work while already committed OCR and audio
artifacts remain reusable.

## Reorder gates

Workers may finish `2,0,3,1`; downstream must receive `0,1,2,3`.

Use a generic pending map + `nextSeq` at both translation and speech completion boundaries.

## Audio buffer

Track buffer in **seconds of playable audio**, not number of files.

Suggested defaults:

- startup target: 20–30 seconds;
- healthy target: 60–120 seconds;
- maximum speculative buffer: 3–5 minutes.

If playback pauses, processing may continue until max buffer, then bounded queues naturally stop upstream work.

## mpv

Start mpv with JSON IPC. Append files using playlist commands as they become ordered-ready. Do not concatenate a growing WAV for streaming playback.

Periodically query current playlist item/time position and atomically save session state.

## Realtime sustainability

Tokens/sec is a diagnostic, not the product metric.

For a processing stage:

`realtime_ratio = produced_playable_audio_seconds / wall_clock_seconds`

Examples:

- 60s future audio translated in 20s -> 3.0x
- 60s in 48s -> 1.25x
- 60s in 80s -> 0.75x

Streaming needs headroom. Initial classification:

- >= 1.50x: healthy
- 1.25–1.50x: sustainable
- 1.00–1.25x: risky
- < 1.00x: cannot sustain indefinitely

These are starting policy values, not eternal constants.

## End-to-end sustainability

Translation and TTS run as a pipeline, so the slowest stage dominates. Track per-stage ratios plus actual buffer slope.

A planner should consider:

- translation realtime ratio EMA;
- TTS realtime ratio EMA;
- current ready-audio seconds;
- buffer trend;
- major-unit boundary distance;
- configured cloud policy.

## Calibration

For translated playback, translate up to the first four segments from the
opening major unit before opening the player. Commit them through the same
translation cache used by the stream. This gives the planner an initial sample
from the actual book and avoids translating the sample twice.

Estimate target speech duration at 150 words per minute until actual synthesis
is available. Divide estimated playable seconds by translation wall time to get
the initial translation realtime ratio. Classify ratios as healthy (at least
1.50x), sustainable (1.25x to below 1.50x), risky (1.00x to below 1.25x), or
unsustainable (below 1.00x). If the opening local probe is unsustainable,
Slice 8 checks cloud policy before playback: `never` stays local, `ask`
requires interactive approval, and `allowed` may select the configured cloud
provider. The chosen provider/model remains fixed for the full operation and
each major unit. `--export` and other non-playback runs do not impose a
realtime cutoff.

As the stream proceeds, update exponential moving averages for translation
audio estimated from newly translated text and for speech generated audio
measured from actual WAV durations. Use the latter to calibrate future spoken
duration estimates per target locale. Cached translation work does not count as
fresh throughput.

After ordered audio enters the player, measure ready audio in seconds using the
player's current position. Continue sampling about once per second while the
stream is active, including while the producer is stalled. Track the observed
buffer slope in seconds gained or lost per wall second and estimate time to
underrun when the trend is negative. Warn once when predicted underrun is
within 45 seconds. The prediction is advisory because batch completion and
player position are bursty.

`listen setup` may store machine/model baseline benchmarks, but each document
session recalibrates from actual translated segments because language pair and
content affect speed.

## Fallback boundary

Do not oscillate providers paragraph-by-paragraph. The current implementation
selects one provider before playback and pins it across every major unit in the
operation. Each batch carries the same glossary and style context.

If later local measurements warn that the buffer may underrun, continue with
the already pinned provider and surface a warning; never change cloud policy or
send source text silently while a unit is in flight.

## Export mode

`--export` runs the same queues and caches but has no playback sink and no realtime deadline. Local translation may therefore be acceptable even when it is too slow for streaming.
