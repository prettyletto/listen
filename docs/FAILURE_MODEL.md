# Failure model

## Source errors

Malformed/unsupported input fails before expensive work when possible. Parser warnings are separate from fatal errors.

## Translation

Do not retry a failed cloud request automatically because it can duplicate
billable work. A failed segment blocks ordered commit at that sequence while
completed local cache entries remain safe. Provider errors report an HTTP
status or a short network/credential hint; response bodies and credentials are
not included. Provider/model selection stays fixed for the operation.

## TTS

A failed speech segment similarly blocks commit. The worker may restart once after process death, then retry the request from cache-safe input.

## Playback

If mpv exits normally, stop speculative generation after a small grace period unless export was also requested. If mpv crashes, preserve session/cache and report a playback error without deleting audio.

## Disk full

Atomic writes may fail. Do not replace a good cache entry/manifest until the new file is fully written and fsynced. Surface path and cleanup guidance.

## Model unavailable

Model load failure is capability failure, not document corruption. Planner may choose another configured backend according to policy.

## Network unavailable

When cloud is selected and unavailable, never discard local caches. If the
cloud adapter cannot start before translation begins, the app continues with
local translation when available. A failure after a cloud request begins stops
that operation; it does not silently switch providers or upload to another
service.
