# Kokoro worker protocol v1

The worker is a long-lived Python process. Go starts it once, reads the ready event, then exchanges newline-delimited JSON on stdin/stdout. Diagnostics go to stderr only.

## Worker ready

```json
{"type":"ready","protocol":1,"engine":"kokoro","sample_rate":24000}
```

## Synthesize request

```json
{"type":"synthesize","request_id":"req-42","segment_id":"seg-42","seq":42,"text":"Text to speak.","voice":"pf_dora","speed":1.0,"output":"/absolute/cache/path/42.wav"}
```

The worker writes to `output + .partial`, fsyncs/closes, validates non-empty PCM, and atomically renames.

## Success

```json
{"type":"synthesized","request_id":"req-42","segment_id":"seg-42","seq":42,"path":"/absolute/cache/path/42.wav","duration_ms":12840}
```

## Error

```json
{"type":"error","request_id":"req-42","code":"synthesis_failed","message":"human-readable error"}
```

## Rules

- stdout is protocol-only. Never print logs there.
- Requests may be sequential in v1. Concurrency belongs to Go unless an ADR changes it.
- A request ID is unique per worker lifetime.
- A segment ID is stable across restarts.
- The worker must survive a failed request when safe.
- Go may terminate the worker on context cancellation.
