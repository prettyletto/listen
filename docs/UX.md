# UX contract

## Golden path

After one-time setup:

```sh
listen book.epub
```

Expected behavior:

1. Detect source and previous session.
2. Parse or load cached structure.
3. Resolve language and default voice.
4. Resume an unfinished session or begin a new source immediately.
5. Generate enough ordered audio for a safe initial buffer.
6. Start playback and continue processing ahead.
7. Show the current chapter and position; allow pause/resume, previous/next
   chapter, and stop.
8. Persist progress and completed work.
9. A later invocation offers the saved position from the listening home or
   resumes directly when a source path is supplied.

No model picker appears on the golden path.

## Listening home

With no arguments in an interactive Linux terminal, `listen` opens a local
home with one prominent Continue listening action for the latest unfinished
source, followed by searchable recent sources. The featured source appears only
once; Recent sources contains the rest of the library. Exactly one item is
focused at a time, and Enter acts on that item. The focused Continue action is
shown first; Down moves to the first recent source and Up returns to Continue.
The recent count and a “more” cue appear when the list continues below the
visible rows. Search keeps its query visible and explains when nothing matches;
Escape clears the query before leaving the search.
Recent rows say Continue, Start listening, or Start again according to their
saved progress. A known chapter or PDF page is labeled “Saved at …”; when the
checkpoint has no reliable chapter or page title, the home says “Continue from
your saved place” instead of presenting the document title as a chapter.
Visible home
actions open path entry (`a`), browsing (`b`), setup or repair (`s`), and
listening settings (`p`). `/` searches the recent list and `q` exits. In the
browser, Enter opens a file or navigates into a folder; `f`
selects the current folder as a documentation source, `.` toggles hidden files,
and Backspace goes up. `?` shows contextual help. A missing recent source offers
Locate, Remove from home, or Back; removing affects only the home entry and `u`
undoes the removal. The home stores source references and display metadata under
XDG data storage; it never copies source contents. Direct source commands start
the supplied source immediately.

Archive-style filenames are shortened only when they match the recognized
author/year/publisher/identifier form with a LibGen site suffix. Other filename
text and parser-supplied document titles remain intact.

During playback, `c` opens the chapter browser with primary chapter stops shown
first. Press `/` to search nested sections in the complete outline; Enter starts
at the selected boundary. Chapters grouped beneath Parts remain in the main
list; appendix entries are searchable. `p` and `n` use the same primary stops,
while flat chapter lists keep their existing entries. Browsing pauses audio and
Escape restores the prior playing or paused state. Player Escape stops, saves
the current position, and returns to home; `q` or Ctrl-C saves and exits.
Non-terminal no-argument invocation continues to show command usage.

Scanned PDFs save the current stable passage, PDF page, and offset within the
passage. On return, the player shows cached-page restoration progress while it
rebuilds the streamed sequence; Escape cancels restoration and keeps the saved
checkpoint. Earlier passages are skipped during resumed speech and playback.
Changed PDF content resumes near the saved page from the beginning of a
passage, with a message identifying the adjusted page. New PDF sessions retain
early first-page playback.

The player shows the physical PDF page when the active streamed passage can be
resolved. Its elapsed/total audio clock is explicitly passage-local; it is not
presented as chapter or book duration.

Chapter selection is an optional player action. Starting a primary chapter or
searched nested section begins at its first segment and continues through the
remaining source. Chapter changes cancel the active stream and restart from the
selected boundary, preserving cached work and sequence ordering. In
non-interactive runs, use the saved position or start from the beginning
without prompting.

## Setup

Choose **Setup / repair** from the listening home to prepare or repair local
speech before selecting a book. Setup explicitly says it needs no source and
returns to the home when complete. When a selected source cannot start because
local speech is not ready, Listen offers **Start setup**, customization, or
Back. Installation begins only after the user chooses Start. Progress is
cancellable; cancellation stops the installer and retains a source selected
from that flow for retry. After successful source-first setup, the selected
source starts or resumes immediately.

Press `p` from home to open listening settings. The screen shows whether local
speech is ready and gives a direct setup/repair action. Users can choose a
default voice and a voice for each language, preview a voice with a short local
sample, and adjust speech-generation and playback speeds. **Advanced settings**
lets users change PDF scan languages and choose whether translation asks before
remote use, stays local, or can use remote translation without another prompt.
Voice and speech-generation speed apply to the next session. Playback speed can
change during playback with `+` and `-`, and the chosen rate is saved for later
sessions. Leaving a preview stops its speech worker and audio player.
Voice-language selection does not enable translation; translation stays
explicit per source and follows the existing cloud-consent policy.

`listen setup` owns dependency/model/voice setup. Every interactive invocation
opens a guided preferences flow; pressing Enter keeps the shown default. It
should:

- verify mpv and ffmpeg or install guidance;
- provision the private Python inference-worker environment for Kokoro and any
  explicitly selected local translator;
- show listening languages and let the user select one or more;
- download only required voices/resources;
- let the user choose a default voice for each selected language;
- offer local English-to-Brazilian-Portuguese translation, or accept the
  explicit `--translate en:pt-BR` option;
- install and health-check the optional local translator;
- measure translation viability against the opening segments of the actual
  source before translated playback;
- keep cloud fallback at `ask` by default, with `never` and `allowed` available
  through `listen config`;
- persist config.

Non-interactive setup uses saved or default choices and does not prompt.
`listen config` displays saved preferences and accepts explicit changes.
Advanced diagnostics belong in `listen doctor`.

## Commands

Required public surface:

```text
listen setup
listen <source>
listen <source> --translate pt-BR
listen <source> --export
listen voices
listen models
listen config
listen doctor
```

Useful later:

```text
listen inspect <source>
listen <source> --from "Chapter 4"
listen <source> --model translategemma-4b
--include-code
--verbose
```

`listen inspect`, `--from`, `--model`, `--include-code`, and `--verbose` are
planned controls, not currently implemented. `--voice <id>` is supported as a
one-shot override. Directory paths and `-` for standard input are already
accepted as sources.

## Progressive disclosure

`listen models` currently discovers OpenAI translation model IDs, and `listen
config` manages voice, speed, and cloud fallback settings. Local translator
discovery and worker-count controls remain future work. Normal playback should
not expose provider internals.

## Error philosophy

Default errors answer three things only:

1. What could not be done?
2. Is existing work safe?
3. What one action fixes it?

If a future verbose mode is added, it can include subprocess stderr, model
paths, HTTP status, timings, cache keys, and parser diagnostics.

## Cloud consent

Content is never sent remotely merely because local translation is slow.

Policies:

- `never`: local only; degrade to wait/export if unsustainable.
- `ask`: prompt before the first remote request for this operation; absent or
  non-interactive input means decline.
- `allowed`: configured provider may be used automatically.

The initial setup defaults to `ask` unless the user explicitly chooses otherwise.

## Translation fallback UX

A model can fit and still be useless for streaming. If local throughput is below the safety threshold, the user sees a simple decision, not token/s internals:

```text
Local translation will probably fall behind playback.
Allow OpenAI to receive this book's text for translation? [y/N]
```

The prompt names the remote action before any source text is sent. Under
`allowed`, the same switch happens without a prompt. The opening local probe
selects one provider for the whole operation, keeping every major unit on the
same backend and preserving cache/resume identity.

Realtime ratios and model details are not currently exposed through a verbose
CLI mode.

## Export terminology

Use `--export`, not `--no-stream`, as the primary user-facing concept. Streaming is an implementation mechanism; export is the user's goal.
