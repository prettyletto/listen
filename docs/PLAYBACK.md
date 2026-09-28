# Playback

Use mpv as a playback engine, not as the orchestrator.

## Streaming playlist

Go owns the ordered queue. Once initial audio buffer is ready:

1. start mpv with JSON IPC and the first audio segment;
2. append subsequent ready files with `loadfile ... append`/playlist commands;
3. keep only ordered-ready segments visible to mpv;
4. query playback state for resume.

mpv's playlist append semantics make segment-by-segment playback possible without rebuilding a monolithic file.

## Chapter navigation

Maintain mapping from segment sequence to major unit. Next/previous chapter commands seek to the first audio segment of the corresponding unit, whether the full chapter has already generated or needs priority generation.

## Priority generation

If the user jumps ahead:

- cancel speculative work that is not reusable only if necessary;
- raise requested major unit to the front of the generation scheduler;
- preserve deterministic sequence within the selected playback path;
- do not delete already cached artifacts.

This is post-v1 unless simple chapter navigation comes for free.

## Resume

Persist current segment and time offset atomically before playback starts and
poll mpv about once per second while it runs. On restart, map the saved segment
ID into the current plan and load the first resumed file at its saved offset.
If an edit removed that segment, resume at the nearest segment in the same
major unit or section and start that segment from the beginning. Reused audio
remains governed by the same content-derived speech cache as a new run.

Export does not start mpv. It drains the same ordered pipeline into the
existing speech cache, then assembles a complete bundle from those exact audio
segments. Only a fully written build is made current.
