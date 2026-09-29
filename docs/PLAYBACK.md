# Playback

Use mpv as a playback engine, not as the orchestrator.

## Start selection and player controls

In an interactive Linux terminal, a document with multiple playable major
units opens a start picker before speech generation begins. The default choice
reads the whole book from its beginning when there is no unfinished session.
With an unfinished session, the picker defaults to the saved position. It also
offers the full book and each playable chapter or major section. Choosing a
chapter starts at its first segment and continues through the rest of the
document. The book title is shown once above concise chapter names. Use
`Up`/`Down` or `j`/`k` to move, `/` to search chapter names, `Enter` to choose,
and `Esc` to clear an active search. Non-interactive runs use the saved
position when available, or start at the beginning.

During playback, Listen owns the terminal controls while mpv plays audio over
JSON IPC:

- `Space`: pause or resume;
- `Left` / `Right`: seek backward or forward by five seconds;
- `p` / `n`: previous or next chapter or major section;
- `q` or `Ctrl-C`: stop and save the current position.

The player screen shows playback progress within the current passage, the
passage number, how much audio is ready, and the available controls. A seek
into audio that has not been generated yet pauses playback and shows a
buffering message. Listen retries the seek as ordered audio becomes ready,
then resumes if playback was running before the seek. If playback was already
paused, it stays paused. A seek past the end of a completed stream leaves the
current position unchanged and explains why it could not move farther.

Chapter navigation checkpoints the current position, cancels the active
translation/speech stream, and starts a new stream at the selected boundary.
Teardown errors from the replaced stream are ignored so they do not appear as
source failures. A chapter key already queued when a stream reports an error is
honored before that error is shown. Completed translations and audio remain
cached and are reused. Every new stream still commits segments in sequence.

## Streaming playlist

Go owns the ordered queue. Once initial audio buffer is ready:

1. start mpv with JSON IPC and the first audio segment;
2. append subsequent ready files with `loadfile ... append`/playlist commands;
3. keep only ordered-ready segments visible to mpv;
4. query playback state for resume.

mpv's playlist append semantics make segment-by-segment playback possible without rebuilding a monolithic file.

## Chapter navigation

Map every segment sequence to its major unit. Previous and next move to the
first audio segment of the neighboring unit. Listen cancels the active stream
and creates a suffix plan from that boundary; it reuses cache entries and does
not delete completed artifacts. No speculative work from the canceled stream
may be appended after the jump.

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
