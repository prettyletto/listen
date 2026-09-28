# listen-cli

`listen` reads text files aloud using local Kokoro TTS. It runs on the CPU, sends
no text to a TTS API, skips code blocks by default, and keeps completed audio
chunks so an interrupted run can resume. Book-like files produce a reusable
audio folder with a full audiobook, separate chapter MP3s, and a playlist.
Other files with sections retain the terminal picker for choosing a section.

## Install on Arch Linux

Install FFmpeg and mpv if they are not already available, then install this
project as a user command:

```sh
sudo pacman -S --needed ffmpeg mpv
uv tool install --python 3.13 --index https://download.pytorch.org/whl/cpu .
```

`listen` is installed into `~/.local/bin`. Make sure that directory is on your
`PATH` (it is already on this machine's zsh `PATH`). Kokoro's model and voice
files download from Hugging Face the first time you generate audio; later runs
use the local cache. The English tokenizer is installed with the command.
Python 3.13 is used because Kokoro currently requires a
Python version below 3.14. On Linux, the project selects the CPU PyTorch wheel.

## Use

```sh
listen README.md
listen ~/notes/lesson.txt
listen chapter.md --speed 1.2
listen chapter.md --playback-speed 1.25
listen -f chapter.md
listen chapter.md --section 2
listen chapter.md --voice pf_dora --output lesson.mp3
listen chapter.md --no-play --output lesson.mp3
listen chapter.md --output lesson.wav
listen ~/Documents/Books/vara_book-audio
listen ~/Documents/Books/vara_book-audio --full
listen ~/Documents/Books/vara_book-audio --section 3
```

For ordinary sectioned documents, `listen chapter.md` opens a hierarchical
selector. It follows the heading hierarchy; each submenu offers `Read all` and
`Back`, and the first menu offers `Full document`. If you choose a section, it
creates a separate MP3 such as
`chapter-02-background.mp3`; choosing the full document creates `chapter.mp3`.
`-f` or `--full` skips the picker and reads the whole source file. Use
`--section N` to choose a section by its position in the complete outline. In
non-interactive scripts, use `-f` to read everything or `--section N` to
choose a section. The picker uses Gum when installed and otherwise shows a
numbered terminal prompt. Book-like files follow the library workflow below.

For a book with Parts and at least two recognized chapter headings, or a
document with at least two explicit chapter headings, `listen book.md` creates
`book-audio/` beside the source. It contains `current.json`, versioned builds
under `builds/`, a `full-book.mp3`, individual MP3s under `chapters/`, an
`all-chapters.m3u` playlist, and the player key bindings. Existing builds stay
available when the source or voice settings change; `current.json` points to
the newest complete build. Chapter audio reuses the same resumable WAV chunks
as ordinary narration. The first run plays the chapter playlist when playback
is enabled. Press **n / N** for the next chapter and **P** for the previous
chapter. **Space / p** pauses and resumes. Use `--no-play` to create or reuse
every MP3 without starting a player.

Run `listen book-audio/` later to choose the playlist, full audiobook, or one
chapter. `--full` plays the full audiobook directly; `--section N` plays one
chapter directly. You can also open a particular version with
`listen book-audio/builds/<build-id>/`.

The outline recognizes common Markdown, reStructuredText, AsciiDoc, Org, HTML,
and LaTeX headings, plus numbered chapters/sections and standalone uppercase or
title-case headings in plain text. Other UTF-8 text files are accepted too.
Markdown headings become spoken headings and paragraph breaks become short
pauses. Fenced and indented code blocks are skipped. Use `--include-code` to
read them.

The first run stores the section outline as JSON under
`$XDG_CACHE_HOME/listen-cli/outlines/` (defaulting to
`~/.cache/listen-cli/outlines/`). Each run hashes the source with SHA-256; the
outline is reused while the file is unchanged and rebuilt when its contents
change. The JSON contains the headings and line ranges, not a copy of the text.

During generation, an interactive terminal shows a single-line progress bar
with completed chunks, percentage, elapsed time, and an estimate of the time
remaining. It uses the terminal's standard color palette and honors `NO_COLOR`.
When output is redirected, `listen` prints plain progress milestones instead.
After saving regular narration, `listen` opens it in mpv's audio-only terminal player.
Use **Space** to pause, **Left/Right** to seek 5 seconds, **Up/Down** to seek
1 minute, **[ / ]** to change speed by 10%, **{ / }** to halve/double speed,
**Backspace** to reset speed, **9 / 0** for volume, **m** to mute, and **q** to
quit playback. Press **?** in mpv to see its full key list. `--playback-speed`
sets the starting player speed from 0.5 to 3.0; `--speed` controls Kokoro's
generation speed. Use `--no-play` to create the audio file and return without
playing it. Playback requires mpv; the audio file is still saved if mpv is
missing or playback cannot start.

Run `listen --help` for options or `listen --list-voices` for common voice IDs.
Use `--no-resume` to discard cached audio chunks and start the selected text
over. Both the outline and audio caches live under `~/.cache/listen-cli/`;
deleting them frees those caches, while Kokoro's downloaded model remains in
the Hugging Face cache.

## Remove

```sh
uv tool uninstall listen-cli
```
