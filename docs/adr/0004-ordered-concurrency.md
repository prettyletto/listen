# ADR 0004 — concurrent workers, ordered commit

Status: accepted.

Translation/TTS may execute concurrently and complete out of order, but every downstream stage sees only contiguous sequence order through reorder gates.

Reason: throughput without nondeterministic playback.
