"""Command line entry point for ``listen``."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import warnings
from dataclasses import dataclass, field
from pathlib import Path

from listen_cli import __version__
from listen_cli.audio import encode_output, is_complete_wav, write_kokoro_chunk
from listen_cli.sections import get_outline, is_book_matter_title, is_chapter_title, is_part_title, part_label
from listen_cli.text import markdown_to_speech, split_into_chunks
from listen_cli.ui import TerminalUI


SUPPORTED_LANGUAGES = set("abefhip")
VOICE_EXAMPLES = (
    ("American English", "af_heart, af_bella, af_sarah, af_nicole, af_sky, am_adam, am_michael"),
    ("British English", "bf_emma, bf_isabella, bm_george, bm_lewis"),
    ("Brazilian Portuguese", "pf_dora, pm_alex"),
)
BUNDLE_VERSION = 1
PLAYLIST_INPUT_CONFIG = """# Chapter navigation for listen-cli's audiobook playlist.
n playlist-next
N playlist-next
P playlist-prev
"""


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="listen",
        description="Read text aloud with local Kokoro TTS; create a reusable chapter library for books.",
        formatter_class=argparse.ArgumentDefaultsHelpFormatter,
    )
    parser.add_argument("input", nargs="?", type=Path, help="text file to narrate or a listen audio directory to play")
    parser.add_argument("-o", "--output", type=Path, help="output .mp3 or .wav (default: beside input as .mp3)")
    parser.add_argument("-f", "--full", action="store_true", help="read the full file without opening the section picker")
    parser.add_argument("--section", type=int, metavar="N", help="read outline section N or bundle chapter N (1-based)")
    parser.add_argument("--voice", default="af_heart", help="Kokoro voice ID; use pf_dora for Brazilian Portuguese")
    parser.add_argument("--speed", type=float, default=1.0, help="Kokoro speech generation speed from 0.5 to 2.0")
    parser.add_argument(
        "--playback-speed",
        type=float,
        default=1.0,
        metavar="RATE",
        help="initial mpv playback speed from 0.5 to 3.0",
    )
    parser.add_argument("--include-code", action="store_true", help="narrate fenced and indented code blocks")
    parser.add_argument("--no-resume", action="store_true", help="discard cached chunks and generate them again")
    parser.add_argument("--no-play", action="store_true", help="create audio without starting playback")
    parser.add_argument("--list-voices", action="store_true", help="show a few voice IDs and exit")
    parser.add_argument("--version", action="version", version=f"listen-cli {__version__}")
    return parser


def _show_voices(ui: TerminalUI) -> None:
    ui.show_voice_examples(VOICE_EXAMPLES)


def _play_output(
    output: Path,
    playback_speed: float,
    ui: TerminalUI,
    *,
    playlist: bool = False,
    input_config: Path | None = None,
) -> None:
    """Play a completed file in mpv's terminal interface, if available."""
    player = shutil.which("mpv")
    if player is None:
        ui.warning(
            f"Audio saved to {output}, but mpv is needed for playback. "
            "Install it with `sudo pacman -S mpv`, or use --no-play next time."
        )
        return
    if not sys.stdin.isatty() or not sys.stdout.isatty() or not ui.interactive:
        ui.warning(f"Audio saved to {output}; playback needs an interactive terminal. Use --no-play in scripts.")
        return

    ui.playback_controls(playlist=playlist)
    command = [
        player,
        "--no-video",
        "--audio-display=no",
        "--term-osd-bar=yes",
        "--input-terminal=yes",
        f"--speed={playback_speed:g}",
    ]
    if input_config is not None:
        command.append(f"--input-conf={input_config.resolve()}")
    command.extend(("--", str(output.resolve())))
    try:
        result = subprocess.run(command, check=False)
    except KeyboardInterrupt:
        ui.warning(f"Playback stopped; the audio file is saved at {output}.")
        return
    except OSError as error:
        ui.warning(f"Could not start mpv ({error}); the audio file is saved at {output}.")
        return

    if result.returncode == 0:
        ui.success("Playback session ended.")
    else:
        ui.warning(f"mpv exited with status {result.returncode}; the audio file is saved at {output}.")


def _cache_directory(cleaned_text: str, voice: str, speed: float, include_code: bool) -> Path:
    xdg_cache = os.environ.get("XDG_CACHE_HOME")
    cache_home = Path(xdg_cache).expanduser() if xdg_cache else Path.home() / ".cache"
    if not cache_home.is_absolute():
        cache_home = Path.home() / cache_home
    payload = {
        "cache_version": 1,
        "kokoro": "0.9.x",
        "text": cleaned_text,
        "voice": voice,
        "speed": speed,
        "include_code": include_code,
    }
    digest = hashlib.sha256(json.dumps(payload, ensure_ascii=False, sort_keys=True).encode("utf-8")).hexdigest()
    return cache_home / "listen-cli" / "jobs" / digest


@dataclass
class PreparedTrack:
    number: int
    section_index: int
    title: str
    label: str
    start_line: int
    end_line: int
    chunks: list[str]
    chunk_paths: list[Path]
    audio_relative_path: str


@dataclass
class AudioBundle:
    directory: Path
    build_directory: Path
    manifest: dict[str, object]
    full_audio: Path
    playlist: Path
    input_config: Path
    chapters: list[tuple[dict[str, object], Path]]


def _atomic_json(path: Path, value: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            "w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}-", suffix=".tmp", delete=False
        ) as temporary:
            temporary_path = Path(temporary.name)
            json.dump(value, temporary, ensure_ascii=False, indent=2)
            temporary.write("\n")
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_path, path)
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def _atomic_text(path: Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            "w", encoding="utf-8", newline="\n", dir=path.parent,
            prefix=f".{path.name}-", suffix=".tmp", delete=False
        ) as temporary:
            temporary_path = Path(temporary.name)
            temporary.write(value)
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_path, path)
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def _safe_bundle_file(build_directory: Path, relative: object) -> Path:
    if not isinstance(relative, str) or not relative:
        raise ValueError("The audio bundle manifest contains an invalid file path.")
    base = build_directory.resolve()
    candidate = (build_directory / relative).resolve()
    try:
        candidate.relative_to(base)
    except ValueError as error:
        raise ValueError("The audio bundle manifest points outside its bundle directory.") from error
    if not candidate.is_file():
        raise ValueError(f"The audio bundle is incomplete; missing {candidate}.")
    return candidate


def _load_audio_bundle(directory: Path) -> AudioBundle:
    """Load and validate a current bundle directory or one build directory."""
    directory = directory.expanduser().resolve()
    current_path = directory / "current.json"
    if current_path.is_file():
        try:
            current = json.loads(current_path.read_text(encoding="utf-8"))
            relative_manifest = current["manifest"]
        except (OSError, ValueError, KeyError, TypeError) as error:
            raise ValueError(f"Could not read audio bundle pointer: {current_path}") from error
        if not isinstance(relative_manifest, str):
            raise ValueError(f"Invalid audio bundle pointer: {current_path}")
        manifest_path = (directory / relative_manifest).resolve()
        try:
            manifest_path.relative_to(directory.resolve())
        except ValueError as error:
            raise ValueError("The audio bundle pointer resolves outside its directory.") from error
    else:
        manifest_path = directory / "manifest.json"

    if not manifest_path.is_file():
        raise ValueError(f"No listen audio bundle was found in {directory}.")
    build_directory = manifest_path.parent
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, ValueError, TypeError) as error:
        raise ValueError(f"Could not read audio bundle manifest: {manifest_path}") from error
    if not isinstance(manifest, dict) or manifest.get("bundle_version") != BUNDLE_VERSION:
        raise ValueError("This audio bundle uses an unsupported manifest version.")

    full_audio = _safe_bundle_file(build_directory, manifest.get("full_audio"))
    playlist = _safe_bundle_file(build_directory, manifest.get("playlist"))
    input_config = _safe_bundle_file(build_directory, manifest.get("player_controls"))
    raw_chapters = manifest.get("chapters")
    if not isinstance(raw_chapters, list) or not raw_chapters:
        raise ValueError("The audio bundle does not contain any chapter tracks.")
    chapters: list[tuple[dict[str, object], Path]] = []
    for item in raw_chapters:
        if not isinstance(item, dict) or not isinstance(item.get("label"), str):
            raise ValueError("The audio bundle contains an invalid chapter entry.")
        chapters.append((item, _safe_bundle_file(build_directory, item.get("audio"))))
    return AudioBundle(directory, build_directory, manifest, full_audio, playlist, input_config, chapters)


def _make_book_tracks(
    raw_text: str,
    roots: list[SectionNode],
    source: Path,
    voice: str,
    speed: float,
    include_code: bool,
) -> list[PreparedTrack]:
    lines = raw_text.splitlines()
    specs = _book_track_specs(roots)
    tracks: list[PreparedTrack] = []
    for node, label, parent_intro in specs:
        start_line = int(node.section["start_line"])
        end_line = int(node.section["end_line"])
        # Keep title pages or other material before the first recognized
        # heading in the opening track instead of dropping them from the book.
        slice_start = 0 if not tracks else start_line - 1
        source_part = "\n".join(lines[slice_start:end_line])
        cleaned = markdown_to_speech(source_part, include_code=include_code, source_format=source.suffix.lower())
        if parent_intro:
            normalized_segment = re.sub(r"\W+", " ", cleaned.casefold(), flags=re.UNICODE).strip()
            missing_intro: list[str] = []
            for intro_line in parent_intro.splitlines():
                intro = markdown_to_speech(intro_line, include_code=include_code, source_format=source.suffix.lower())
                normalized_intro = re.sub(r"\W+", " ", intro.casefold(), flags=re.UNICODE).strip()
                if normalized_intro and normalized_intro not in normalized_segment:
                    missing_intro.append(intro_line)
            if missing_intro:
                cleaned = markdown_to_speech(
                    "\n\n".join((*missing_intro, source_part)),
                    include_code=include_code,
                    source_format=source.suffix.lower(),
                )
        chunks = split_into_chunks(cleaned)
        if not chunks:
            continue
        slug = re.sub(r"[^\w-]+", "-", node.title.casefold(), flags=re.UNICODE).strip("-")[:48].rstrip("-")
        slug = slug or "chapter"
        audio_relative_path = f"chapters/{len(tracks) + 1:03d}-{slug}.mp3"
        cache_dir = _cache_directory(cleaned, voice, speed, include_code)
        chunk_paths = [cache_dir / f"chunk-{index:05d}.wav" for index in range(len(chunks))]
        tracks.append(
            PreparedTrack(
                number=len(tracks) + 1,
                section_index=node.index,
                title=node.title,
                label=label,
                start_line=1 if not tracks else start_line,
                end_line=end_line,
                chunks=chunks,
                chunk_paths=chunk_paths,
                audio_relative_path=audio_relative_path,
            )
        )
    return tracks


def _book_build_id(source: Path, source_sha256: str, voice: str, speed: float, include_code: bool) -> str:
    payload = {
        "bundle_version": BUNDLE_VERSION,
        "source_path": str(source.resolve()),
        "source_format": source.suffix.lower(),
        "source_sha256": source_sha256,
        "voice": voice,
        "synthesis_speed": speed,
        "include_code": include_code,
    }
    return hashlib.sha256(json.dumps(payload, sort_keys=True).encode("utf-8")).hexdigest()


def _point_to_build(bundle_directory: Path, build_id: str) -> None:
    _atomic_json(
        bundle_directory / "current.json",
        {"bundle_version": BUNDLE_VERSION, "build_id": build_id, "manifest": f"builds/{build_id}/manifest.json"},
    )


def _build_book_bundle(
    source: Path,
    raw_text: str,
    sections: list[dict[str, object]],
    source_sha256: str,
    args: argparse.Namespace,
    ui: TerminalUI,
) -> AudioBundle:
    roots = _section_tree(sections)
    tracks = _make_book_tracks(raw_text, roots, source, args.voice, args.speed, args.include_code)
    if not tracks:
        raise ValueError("This book has no readable top-level chapters or sections.")
    if shutil.which("ffmpeg") is None:
        raise RuntimeError("ffmpeg is required to create MP3 files. Install it with: sudo pacman -S ffmpeg")

    build_id = _book_build_id(source, source_sha256, args.voice, args.speed, args.include_code)
    bundle_directory = source.with_name(f"{source.stem}-audio")
    build_directory = bundle_directory / "builds" / build_id
    manifest_path = build_directory / "manifest.json"

    if not args.no_resume and manifest_path.is_file():
        try:
            cached_bundle = _load_audio_bundle(build_directory)
            if (
                cached_bundle.manifest.get("source_sha256") == source_sha256
                and cached_bundle.manifest.get("voice") == args.voice
                and cached_bundle.manifest.get("synthesis_speed") == args.speed
                and cached_bundle.manifest.get("include_code") is args.include_code
                and len(cached_bundle.chapters) == len(tracks)
            ):
                _point_to_build(bundle_directory, build_id)
                ui.success(f"Using completed audio bundle with {len(tracks)} track(s).")
                return _load_audio_bundle(bundle_directory)
        except ValueError:
            # An incomplete previous attempt is resumed from its chunk cache.
            pass

    bundle_directory.mkdir(parents=True, exist_ok=True)
    build_directory.mkdir(parents=True, exist_ok=True)
    (build_directory / "chapters").mkdir(parents=True, exist_ok=True)
    if args.no_resume:
        for cache_dir in {path.parent for track in tracks for path in track.chunk_paths}:
            shutil.rmtree(cache_dir, ignore_errors=True)

    total_chunks = sum(len(track.chunks) for track in tracks)
    if total_chunks < 1:
        raise ValueError("No readable text was found in the book.")
    for track in tracks:
        track.chunk_paths[0].parent.mkdir(parents=True, exist_ok=True)
    cached_paths = {
        path: is_complete_wav(path)
        for track in tracks
        for path in track.chunk_paths
    }
    if all(cached_paths.values()):
        ui.success(f"Using {total_chunks} completed audio chunks from cache.")
    else:
        pipeline = ui.run_with_status(
            "Loading local Kokoro model and voice (the first run may download them)…",
            lambda: _create_pipeline(args.voice[0].lower()),
        )
        with ui.chunk_progress(total_chunks) as progress:
            for track in tracks:
                short_label = track.label[:48]
                for chunk_number, (chunk, path) in enumerate(zip(track.chunks, track.chunk_paths), start=1):
                    if cached_paths[path]:
                        progress.advance(f"Reusing {short_label}")
                        continue
                    progress.set_description(f"Generating {track.number}/{len(tracks)} · {short_label}")
                    write_kokoro_chunk(pipeline, chunk, args.voice, args.speed, path)
                    cached_paths[path] = True
                    progress.advance(f"Generating {track.number}/{len(tracks)} · chunk {chunk_number}")

    chapter_manifest: list[dict[str, object]] = []
    playlist_lines = ["#EXTM3U"]
    all_chunk_paths: list[Path] = []

    def assemble_outputs() -> None:
        for track in tracks:
            output_path = build_directory / track.audio_relative_path
            if args.no_resume or not output_path.is_file() or output_path.stat().st_size == 0:
                encode_output(track.chunk_paths, output_path)
            all_chunk_paths.extend(track.chunk_paths)
            chapter_manifest.append(
                {
                    "number": track.number,
                    "section_index": track.section_index,
                    "title": track.title,
                    "label": track.label,
                    "start_line": track.start_line,
                    "end_line": track.end_line,
                    "audio": track.audio_relative_path,
                }
            )
            playlist_lines.extend((f"#EXTINF:-1,{track.label.replace(',', ' ')}", track.audio_relative_path))

        full_path = build_directory / "full-book.mp3"
        if args.no_resume or not full_path.is_file() or full_path.stat().st_size == 0:
            encode_output(all_chunk_paths, full_path)

    ui.run_with_status(
        f"Assembling the full audiobook and {len(tracks)} chapter tracks…",
        assemble_outputs,
    )

    _atomic_text(build_directory / "all-chapters.m3u", "\n".join(playlist_lines) + "\n")
    _atomic_text(build_directory / "controls.input.conf", PLAYLIST_INPUT_CONFIG)
    manifest: dict[str, object] = {
        "bundle_version": BUNDLE_VERSION,
        "build_id": build_id,
        "source": str(source.resolve()),
        "source_sha256": source_sha256,
        "voice": args.voice,
        "synthesis_speed": args.speed,
        "include_code": args.include_code,
        "full_audio": "full-book.mp3",
        "playlist": "all-chapters.m3u",
        "player_controls": "controls.input.conf",
        "chapters": chapter_manifest,
    }
    _atomic_json(manifest_path, manifest)
    _point_to_build(bundle_directory, build_id)
    ui.success(f"Prepared {len(tracks)} track(s) in {bundle_directory}.")
    ui.muted(f"Full audiobook: {build_directory / 'full-book.mp3'}")
    return _load_audio_bundle(bundle_directory)


def _play_bundle(bundle: AudioBundle, args: argparse.Namespace, ui: TerminalUI) -> None:
    if args.no_play:
        ui.info(f"Audio bundle is ready at {bundle.directory}")
        return

    if args.full:
        _play_output(bundle.full_audio, args.playback_speed, ui)
        return
    if args.section is not None:
        if args.section > len(bundle.chapters):
            raise ValueError(
                f"Chapter {args.section} does not exist; this bundle has {len(bundle.chapters)} track(s)."
            )
        _play_output(bundle.chapters[args.section - 1][1], args.playback_speed, ui)
        return
    if not sys.stdin.isatty() or not sys.stdout.isatty():
        raise ValueError("Choose a bundle track with --full or --section N when running without a terminal.")

    options = [("a", "All chapters · playlist"), ("f", "Full audiobook")]
    options.extend((str(index), item["label"]) for index, (item, _) in enumerate(bundle.chapters, start=1))
    choice = _select_menu(options, "Listen to this book", "a")
    if choice is None:
        ui.warning("Selection cancelled.")
    elif choice == "a":
        _play_output(bundle.playlist, args.playback_speed, ui, playlist=True, input_config=bundle.input_config)
    elif choice == "f":
        _play_output(bundle.full_audio, args.playback_speed, ui)
    else:
        _play_output(bundle.chapters[int(choice) - 1][1], args.playback_speed, ui)


def _open_audio_directory(directory: Path, args: argparse.Namespace, ui: TerminalUI) -> int:
    if args.output is not None:
        raise ValueError("--output is only valid when converting a text file.")
    bundle = _load_audio_bundle(directory)
    _play_bundle(bundle, args, ui)
    return 0


def _create_pipeline(language: str) -> object:
    try:
        from kokoro import KPipeline
    except ImportError as error:
        raise RuntimeError("Kokoro is not installed. Install this tool with `uv tool install --python 3.13 .`.") from error

    # These three upstream warnings are expected with the current Kokoro
    # architecture and PyTorch APIs; keep the user's CLI output readable.
    with warnings.catch_warnings():
        warnings.filterwarnings(
            "ignore",
            message="dropout option adds dropout after all but last recurrent layer.*",
            category=UserWarning,
        )
        warnings.filterwarnings(
            "ignore",
            category=FutureWarning,
            module=r"torch\.nn\.utils\.weight_norm",
        )
        warnings.filterwarnings(
            "ignore",
            category=FutureWarning,
            module=r"torch\.jit\._script",
        )
        return KPipeline(lang_code=language, repo_id="hexgrad/Kokoro-82M", device="cpu")


def _decode_text(source: Path) -> tuple[bytes, str]:
    try:
        content = source.read_bytes()
    except OSError as error:
        raise ValueError(f"Could not read {source}: {error}") from error

    if b"\x00" in content[:8192] and not content.startswith((b"\xff\xfe", b"\xfe\xff", b"\x00\x00\xfe\xff", b"\xff\xfe\x00\x00")):
        raise ValueError(f"{source} looks like a binary file; listen accepts text files.")
    if content.startswith((b"\x00\x00\xfe\xff", b"\xff\xfe\x00\x00")):
        decoded = content.decode("utf-32", errors="replace")
    elif content.startswith((b"\xff\xfe", b"\xfe\xff")):
        decoded = content.decode("utf-16", errors="replace")
    else:
        decoded = content.decode("utf-8-sig", errors="replace")
    return content, decoded


@dataclass
class SectionNode:
    index: int
    section: dict[str, object]
    children: list[SectionNode] = field(default_factory=list)

    @property
    def title(self) -> str:
        return str(self.section["title"]).replace("\n", " ").strip()

    @property
    def level(self) -> int:
        return int(self.section["level"])


def _section_tree(sections: list[dict[str, object]]) -> list[SectionNode]:
    """Nest each heading under the nearest preceding heading of a lower level."""
    roots: list[SectionNode] = []
    stack: list[SectionNode] = []
    for index, section in enumerate(sections, start=1):
        node = SectionNode(index, section)
        while stack and stack[-1].level >= node.level:
            stack.pop()
        if stack:
            stack[-1].children.append(node)
        else:
            roots.append(node)
        stack.append(node)
    return roots


def _main_selection_nodes(roots: list[SectionNode]) -> list[tuple[SectionNode, str]]:
    """Promote chapter headings out of Part containers for book-like outlines.

    Numbered or explicitly named chapters beneath Parts are strong evidence of
    a book hierarchy. In that case, Parts become labels on their children so a
    reader can choose a chapter immediately. Other outlines retain their
    ordinary heading-level tree.
    """
    parts = [node for node in roots if is_part_title(node.title)]
    chapter_markers = sum(
        is_chapter_title(child.title)
        for part in parts
        for child in part.children
    )
    if not parts or chapter_markers < 2:
        return [(node, node.title) for node in roots]

    choices: list[tuple[SectionNode, str]] = []
    for node in roots:
        if not is_part_title(node.title) or not node.children:
            choices.append((node, node.title))
            continue

        group = part_label(node.title)
        for child in node.children:
            label = child.title
            if not is_book_matter_title(child.title):
                label = f"{group} · {child.title}"
            choices.append((child, label))
    return choices


def _book_track_specs(roots: list[SectionNode]) -> list[tuple[SectionNode, str, str]]:
    """Return ordered major audio tracks for a book-like heading tree.

    Each tuple contains the node to narrate, its display label, and an optional
    parent Part heading to speak before that Part's first track.
    """
    def walk(node: SectionNode) -> list[SectionNode]:
        descendants = [node]
        for child in node.children:
            descendants.extend(walk(child))
        return descendants

    all_nodes = [item for root in roots for item in walk(root)]
    parts = [node for node in all_nodes if is_part_title(node.title)]
    chapter_markers = sum(
        is_chapter_title(child.title)
        for part in parts
        for child in part.children
    )
    chapter_nodes = [node for node in all_nodes if is_chapter_title(node.title)]
    if not ((parts and chapter_markers >= 2) or (not parts and len(chapter_nodes) >= 2)):
        return []

    def has_part_descendant(node: SectionNode) -> bool:
        return any(is_part_title(item.title) for child in node.children for item in walk(child))

    def combine_intro(first: tuple[SectionNode, str, str], intro: str) -> tuple[SectionNode, str, str]:
        if not intro:
            return first
        joined = "\n\n".join(part for part in (intro, first[2]) if part)
        return first[0], first[1], joined

    def expand(node: SectionNode) -> list[tuple[SectionNode, str, str]]:
        if is_part_title(node.title):
            if not node.children:
                return [(node, node.title, "")]
            group = part_label(node.title)
            result: list[tuple[SectionNode, str, str]] = []
            for child in node.children:
                label = child.title if is_book_matter_title(child.title) else f"{group} · {child.title}"
                result.append((child, label, node.title if not result else ""))
            return result

        if has_part_descendant(node):
            result = []
            for child in node.children:
                result.extend(expand(child))
            if result:
                result[0] = combine_intro(result[0], node.title)
                return result

        chapter_children = [child for child in node.children if is_chapter_title(child.title)]
        if len(chapter_children) >= 2:
            result = [(child, child.title, "") for child in chapter_children]
            result[0] = combine_intro(result[0], node.title)
            return result

        return [(node, node.title, "")]

    result: list[tuple[SectionNode, str, str]] = []
    for root in roots:
        result.extend(expand(root))
    return result


def _select_menu(options: list[tuple[str, str]], header: str, default_key: str) -> str | None:
    """Show one menu and return its key, or None when the user cancels."""
    choices = [f"{key}) {label}" for key, label in options]
    menu_ui = TerminalUI(sys.stdout)

    # Gum supplies a compact, scrollable selector when installed. Keep stderr
    # attached to the terminal so its interactive screen remains visible.
    gum = shutil.which("gum")
    if gum and sys.stdin.isatty() and sys.stdout.isatty():
        try:
            result = subprocess.run(
                [gum, "choose", "--limit", "1", "--height", "12", "--header", header, *choices],
                stdin=None,
                stdout=subprocess.PIPE,
                text=True,
                check=False,
            )
        except OSError:
            result = None
        if result is not None:
            if result.returncode == 0:
                match = re.match(r"\s*([a-z0-9]+)\)", result.stdout, re.IGNORECASE)
                if match:
                    return match.group(1).casefold()
            elif result.returncode == 130:
                return None
            elif result.returncode != 1:
                TerminalUI().warning("Terminal selector unavailable; using a numbered prompt.")

    menu_ui.menu(header, options, default_key)
    valid_keys = {key.casefold() for key, _ in options}
    while True:
        try:
            choice = menu_ui.selection_prompt(default_key)
        except EOFError:
            return None
        if not choice:
            return default_key
        if choice in {"q", "quit", "cancel"}:
            return None
        if choice in valid_keys:
            return choice
        TerminalUI().warning("Choose one of the listed options, or q to cancel.")


_BACK = "__back__"


def _select_node(node: SectionNode) -> int | str | None:
    """Drill through a section's children, offering the parent as a whole."""
    if not node.children:
        return node.index
    children = {str(index): child for index, child in enumerate(node.children, start=1)}
    options = [("0", f"Read all: {node.title}")]
    options.extend((key, child.title) for key, child in children.items())
    options.append(("b", "Back"))

    while True:
        key = _select_menu(options, f"Inside {node.title}", "0")
        if key is None:
            return None
        if key == "b":
            return _BACK
        if key == "0":
            return node.index
        child = children[key]
        if not child.children:
            return child.index
        result = _select_node(child)
        if result == _BACK:
            continue
        return result


def _select_section(sections: list[dict[str, object]]) -> int | None:
    """Show chapters first for books with Parts, then drill into subsections."""
    roots = _section_tree(sections)
    main_nodes = _main_selection_nodes(roots)
    root_sections = {str(index): node for index, (node, _) in enumerate(main_nodes, start=1)}
    options = [("0", "Full document")]
    options.extend(
        (str(index), label)
        for index, (_, label) in enumerate(main_nodes, start=1)
    )

    while True:
        key = _select_menu(options, "Chapters and book sections", "0")
        if key is None:
            return None
        if key == "0":
            return 0
        node = root_sections[key]
        if not node.children:
            return node.index
        result = _select_node(node)
        if result == _BACK:
            continue
        return result if isinstance(result, int) else None


def _section_output(source: Path, section_number: int, title: str) -> Path:
    slug = re.sub(r"[^\w-]+", "-", title.casefold(), flags=re.UNICODE).strip("-")
    slug = slug[:48].rstrip("-") or "section"
    return source.with_name(f"{source.stem}-{section_number:02d}-{slug}.mp3")


def _run(args: argparse.Namespace, parser: argparse.ArgumentParser, ui: TerminalUI | None = None) -> int:
    ui = ui or TerminalUI()
    if args.list_voices:
        _show_voices(ui)
        return 0
    if args.input is None:
        parser.error("provide a text file (or use --list-voices)")
    if args.full and args.section is not None:
        parser.error("--full and --section cannot be used together")
    if args.section is not None and args.section < 1:
        parser.error("--section must be a positive number")
    if not 0.5 <= args.playback_speed <= 3.0:
        raise ValueError("Playback speed must be between 0.5 and 3.0.")

    source = args.input.expanduser()
    if source.is_dir():
        return _open_audio_directory(source, args, ui)
    if not source.is_file():
        raise ValueError(f"Input file does not exist: {source}")
    if not 0.5 <= args.speed <= 2.0:
        raise ValueError("Speed must be between 0.5 and 2.0.")
    if len(args.voice) < 2 or args.voice[0].lower() not in SUPPORTED_LANGUAGES:
        raise ValueError("The voice ID must start with a supported Kokoro language prefix (for example af_ or pf_).")

    content, raw_text = _decode_text(source)
    source_sha256 = hashlib.sha256(content).hexdigest()
    sections, outline_hit, _outline_path, outline_saved = get_outline(source, raw_text, source_sha256)
    if outline_hit:
        ui.muted("Using cached section outline.")
    elif outline_saved:
        ui.info(f"Scanned and cached {len(sections)} section headings.")
    else:
        ui.warning("Scanned the section outline, but could not cache it; it will be scanned again next run.")

    book_specs = _book_track_specs(_section_tree(sections))
    if book_specs and not args.full and args.section is None and args.output is None:
        bundle = _build_book_bundle(source, raw_text, sections, source_sha256, args, ui)
        if args.no_play:
            ui.info(f"Audio bundle is ready at {bundle.directory}")
        else:
            _play_output(
                bundle.playlist,
                args.playback_speed,
                ui,
                playlist=True,
                input_config=bundle.input_config,
            )
        return 0

    selected_section: dict[str, object] | None = None
    if args.section is not None:
        if args.section > len(sections):
            raise ValueError(f"Section {args.section} does not exist; this file has {len(sections)} section(s).")
        selected_section = sections[args.section - 1]
    elif not args.full and sections:
        if not sys.stdin.isatty() or not sys.stdout.isatty():
            raise ValueError("This file has sections; use -f/--full or --section N when running without a terminal.")
        choice = _select_section(sections)
        if choice is None:
            ui.warning("Selection cancelled.")
            return 130
        if choice > 0:
            selected_section = sections[choice - 1]

    if selected_section is None:
        text_to_read = raw_text
        section_number = 0
    else:
        source_lines = raw_text.splitlines()
        start = int(selected_section["start_line"]) - 1
        end = int(selected_section["end_line"])
        text_to_read = "\n".join(source_lines[start:end])
        section_number = sections.index(selected_section) + 1

    cleaned = markdown_to_speech(text_to_read, include_code=args.include_code, source_format=source.suffix.lower())
    chunks = split_into_chunks(cleaned)
    if not chunks:
        raise ValueError("No readable text was found in the input file.")

    if args.output:
        output = args.output.expanduser()
    elif selected_section is not None:
        output = _section_output(source, section_number, str(selected_section["title"]))
    else:
        output = source.with_suffix(".mp3")
    if output.resolve() == source.resolve():
        raise ValueError("Output cannot overwrite the input file.")
    if output.suffix.lower() not in {".mp3", ".wav"}:
        raise ValueError("Output must end in .mp3 or .wav.")
    if output.suffix.lower() == ".mp3" and shutil.which("ffmpeg") is None:
        raise RuntimeError("ffmpeg is required to create MP3 files. Install it with: sudo pacman -S ffmpeg")

    cache_dir = _cache_directory(cleaned, args.voice, args.speed, args.include_code)
    if args.no_resume:
        shutil.rmtree(cache_dir, ignore_errors=True)
    cache_dir.mkdir(parents=True, exist_ok=True)
    chunk_paths = [cache_dir / f"chunk-{index:05d}.wav" for index in range(len(chunks))]

    chunk_is_cached = [is_complete_wav(path) for path in chunk_paths]
    if all(chunk_is_cached):
        ui.success(f"Using {len(chunks)} completed audio chunks from cache.")
    else:
        pipeline = ui.run_with_status(
            "Loading local Kokoro model and voice (the first run may download them)…",
            lambda: _create_pipeline(args.voice[0].lower()),
        )
        with ui.chunk_progress(len(chunks)) as progress:
            for chunk, path, cached in zip(chunks, chunk_paths, chunk_is_cached):
                if cached:
                    progress.advance("Reusing cached audio")
                    continue
                progress.set_description("Generating audio")
                write_kokoro_chunk(pipeline, chunk, args.voice, args.speed, path)
                progress.advance("Generating audio")

    ui.run_with_status(
        f"Joining audio chunks and writing {output}…",
        lambda: encode_output(chunk_paths, output),
    )
    ui.saved(output)
    if not args.no_play:
        _play_output(output, args.playback_speed, ui)
    return 0


def main() -> None:
    parser = _parser()
    args = parser.parse_args()
    ui = TerminalUI()
    try:
        code = _run(args, parser, ui)
    except KeyboardInterrupt:
        ui.warning("Interrupted. Completed chunks are saved; rerun the same command to resume.")
        code = 130
    except Exception as error:
        ui.error(f"listen: {error}")
        code = 1
    raise SystemExit(code)
