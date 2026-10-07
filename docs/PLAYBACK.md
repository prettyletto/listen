# Playback

Use mpv as a playback engine, not as the orchestrator.

## Listening home and player controls

In an interactive Linux terminal, `listen` with no arguments opens the local
listening home. Its prominent Continue listening action opens the latest
unfinished source in one Enter. The featured source appears only once; Recents
contains the remaining sources and offers Continue, Start listening, or Start
again. Enter acts on the one focused item, and Up/Down move between Continue and
the recent-source list. A known chapter or PDF page is labeled “Saved at …”; if
the checkpoint has no reliable content location, the home says “Continue from
your saved place.” Use `a` to enter or paste a path, `b` to browse from the current
directory, `/` to search, and `q` to exit. The browser opens files, navigates
folders, selects the current folder as documentation with `f`, toggles hidden
files with `.`, and goes up with Backspace. `?` shows contextual help. Missing
sources offer Locate, Remove from home, and Back; `u` undoes a removal. Running
`listen <source>` starts or resumes that source immediately without a start
picker. Non-terminal runs also start from the saved position when available,
or from the beginning.

During playback, Listen owns the terminal controls while mpv plays audio over
JSON IPC:

- `Space`: pause or resume;
- `Left` / `Right`: seek backward or forward by five seconds;
- `p` / `n`: previous or next primary chapter stop, when available;
- `c`: open the chapter browser, when available;
- `+` / `-`: increase or decrease playback speed by 0.1× and save the rate;
- `Esc`: stop, save the current position, and return to the listening home;
- `q` or `Ctrl-C`: stop, save, and exit the app.

The player screen prioritizes source title, chapter or reading order, resolved
PDF page, playback state, controls, and progress. Its elapsed/total audio clock
is explicitly labeled passage time; it does not imply chapter or book duration.
It also shows the passage number and how much audio is ready. While
Listen prepares the first audio, changes chapters, buffers a seek, or builds
later audio in the background, an animated indicator appears beside the
current activity and the ready count continues to show completed work. During
initial preparation, Escape returns home and `q` remains available to exit;
playback controls become available when playback starts. A seek into audio that has not been generated
yet pauses playback and shows a buffering message. Listen retries the seek as
ordered audio becomes ready, then resumes if playback was running before the
seek. If playback was already paused, it stays paused. A seek past the end of a
completed stream leaves the current position unchanged and explains why it
could not move farther.

Press `p` from the listening home to edit voice mappings and listening speeds.
Voice and speech-generation speed apply to the next session. Playback speed is
saved globally and can also change immediately with `+` and `-` while playing.
The `--voice`, `--speed`, and `--playback-speed` flags override saved defaults
for a direct source command. A voice preview synthesizes a short fixed sample
locally; leaving its screen cancels synthesis and closes the temporary player.

When chapter navigation is available, press `c` to open the chapter browser.
It shows primary chapter stops by default. Press `/` to search the full outline,
including nested sections and appendix entries, and Enter starts at that
section's first segment. Chapters grouped under Parts remain primary stops.
Flat chapter lists remain unchanged. `p` and `n` move between the same primary
stops shown by default. Browsing pauses playback; Escape returns to the player
and restores its previous paused or playing state. Choosing a chapter or nested
section checkpoints the current position, cancels the active translation/speech
stream, and starts a new stream at the selected boundary.
Teardown errors from the replaced stream are ignored so they do not appear as
source failures. A chapter key already queued when a stream reports an error is
honored before that error is shown. Completed translations and audio remain
cached and are reused. Every new stream still commits segments in sequence.

Streamed scanned-PDF playback does not yet have an ordered chapter outline.
During live page recognition the player hides chapter controls from its footer
and contextual help. Pressing a chapter key explains that PDF recognition is
still running; other playback controls remain available.

## Streaming playlist

Go owns the ordered queue. Once initial audio buffer is ready:

1. start mpv with JSON IPC and the first audio segment;
2. append subsequent ready files with `loadfile ... append`/playlist commands;
3. keep only ordered-ready segments visible to mpv;
4. query playback state for resume.

mpv's playlist append semantics make segment-by-segment playback possible without rebuilding a monolithic file.

## Chapter navigation

Map every segment sequence to its major unit. Previous and next move to the
first audio segment of the neighboring primary chapter stop. The browser shows
those same stops by default; searching expands it to nested outline entries,
which still start at their exact first segment. Flat chapter lists keep every
entry as a primary stop. Listen cancels the active stream and creates a suffix
plan from the chosen boundary; it reuses cache entries and does not delete
completed artifacts. No speculative work from the canceled stream may be
appended after the jump.

## Resume

Persist the current segment and time offset atomically and poll mpv about once
per second while it runs. Static plans map the saved segment ID into the
current plan and load the first resumed file at its saved offset. If an edit
removed that segment, resume at the nearest structural location and reset the
offset.

For streamed scanned PDFs, each checkpoint also records the physical page and
passage slot. On restart, Listen traverses cached OCR pages to rebuild the
mapping, showing progress while keeping Escape available. It then skips earlier
passages before synthesis and playback. Changed PDF content resumes at the
nearest passage on the saved page, or the nearest page with readable text, and
resets the offset. If content is unchanged and only the path-sensitive segment
ID changed, page-local mapping preserves the offset. Reused audio remains
governed by the same content-derived speech cache as a new run.

Export does not start mpv. It drains the same ordered pipeline into the
existing speech cache, then assembles a complete bundle from those exact audio
segments. Only a fully written build is made current.
