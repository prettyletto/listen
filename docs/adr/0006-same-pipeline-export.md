# ADR 0006 — playback and export share one processing pipeline

Status: accepted.

`--export` changes the sink/deadline behavior, not parsing/translation/TTS implementation. All stage caches are shared.
