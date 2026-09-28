"""Small, theme-aware terminal UI for the ``listen`` command."""

from __future__ import annotations

import os
import sys
from collections.abc import Callable
from typing import TypeVar

from rich.console import Console
from rich.progress import (
    BarColumn,
    MofNCompleteColumn,
    Progress,
    SpinnerColumn,
    TextColumn,
    TimeElapsedColumn,
    TimeRemainingColumn,
)
from rich.table import Table
from rich.text import Text
from rich.theme import Theme


_RESULT = TypeVar("_RESULT")

_LISTEN_THEME = Theme(
    {
        "listen.brand": "bold cyan",
        "listen.info": "cyan",
        "listen.muted": "dim",
        "listen.success": "green",
        "listen.warning": "yellow",
        "listen.error": "bold red",
        "listen.path": "underline",
        "bar.back": "bright_black",
        "bar.complete": "green",
        "bar.finished": "green",
        "bar.pulse": "cyan",
        "progress.spinner": "cyan",
    },
    inherit=True,
)


class ChunkProgress:
    """Show a live bar in a terminal and sparse plain progress in a pipe."""

    def __init__(self, ui: TerminalUI, total: int) -> None:
        self.ui = ui
        self.total = total
        self.completed = 0
        self._last_tenth = 0
        self._progress: Progress | None = None
        self._task_id: int | None = None

    def __enter__(self) -> ChunkProgress:
        if self.ui.interactive:
            self._progress = Progress(
                SpinnerColumn(style="progress.spinner"),
                TextColumn("{task.description}", style="listen.info"),
                BarColumn(bar_width=18),
                MofNCompleteColumn(),
                TextColumn("{task.percentage:>3.0f}%", style="listen.info"),
                TimeElapsedColumn(),
                TimeRemainingColumn(compact=True),
                console=self.ui.console,
                refresh_per_second=8,
                transient=False,
                expand=False,
            )
            self._task_id = self._progress.add_task("Preparing audio", total=self.total)
            self._progress.start()
        else:
            self.ui.info("Generating audio in chunks")
            self.ui.muted(f"Audio chunks: 0/{self.total} (0%)")
        return self

    def set_description(self, description: str) -> None:
        if self._progress is not None and self._task_id is not None:
            self._progress.update(self._task_id, description=description)

    def advance(self, description: str) -> None:
        self.completed = min(self.completed + 1, self.total)
        if self._progress is not None and self._task_id is not None:
            self._progress.update(self._task_id, advance=1, description=description)
            return

        tenth = self.completed * 10 // self.total
        if tenth > self._last_tenth:
            self._last_tenth = tenth
            percent = self.completed * 100 // self.total
            self.ui.muted(f"Audio chunks: {self.completed}/{self.total} ({percent}%)")

    def __exit__(self, *_: object) -> None:
        if self._progress is not None:
            self._progress.stop()


class TerminalUI:
    """Centralize output styling and terminal capability detection."""

    def __init__(self, stream: object | None = None) -> None:
        output = stream if stream is not None else sys.stderr
        is_tty = bool(getattr(output, "isatty", lambda: False)())
        term_is_dumb = os.environ.get("TERM", "").lower() == "dumb"
        no_color = not is_tty or term_is_dumb or bool(os.environ.get("NO_COLOR"))
        self.interactive = is_tty and not term_is_dumb
        self.console = Console(
            file=output,
            color_system=None if no_color else "standard",
            no_color=no_color,
            theme=_LISTEN_THEME,
            soft_wrap=True,
            markup=False,
            highlight=False,
        )

    def info(self, message: str) -> None:
        self.console.print(Text(message, style="listen.info"))

    def muted(self, message: str) -> None:
        self.console.print(Text(message, style="listen.muted"))

    def success(self, message: str) -> None:
        self.console.print(Text(message, style="listen.success"))

    def warning(self, message: str) -> None:
        self.console.print(Text(message, style="listen.warning"))

    def error(self, message: str) -> None:
        self.console.print(Text(message, style="listen.error"))

    def saved(self, path: object) -> None:
        self.console.print(
            Text.assemble(
                ("Saved ", "listen.success"),
                (str(path), "listen.path"),
            )
        )

    def playback_controls(self, *, playlist: bool = False) -> None:
        if playlist:
            self.info("Chapters: n / N next · P previous · Space / p pause · q quit · ? help")
        self.info("Playback: Space pause · ←/→ seek 5s · ↑/↓ seek 1m · q quit · ? help")
        self.muted("Speed: [ / ] ±10% · { / } halve/double · Backspace reset · Volume: 9 / 0 · m mute")

    def menu(self, header: str, options: list[tuple[str, str]], default_key: str) -> None:
        self.console.print(Text(header, style="listen.brand"))
        for key, label in options:
            if key.casefold() == default_key.casefold():
                prefix, key_style = "› ", "listen.brand"
            else:
                prefix, key_style = "  ", "listen.muted"
            self.console.print(
                Text.assemble(
                    (prefix, key_style),
                    (f"{key}) ", "listen.info"),
                    (label, None),
                )
            )

    def selection_prompt(self, default_key: str) -> str:
        self.console.print(
            Text.assemble(
                ("Selection ", "listen.muted"),
                (f"[{default_key}]: ", "listen.brand"),
            ),
            end="",
        )
        return input().strip().casefold()

    def run_with_status(self, message: str, operation: Callable[[], _RESULT]) -> _RESULT:
        if self.interactive:
            with self.console.status(Text(message, style="listen.info"), spinner="dots"):
                return operation()
        self.info(message)
        return operation()

    def chunk_progress(self, total: int) -> ChunkProgress:
        return ChunkProgress(self, total)

    def show_voice_examples(self, examples: tuple[tuple[str, str], ...]) -> None:
        table = Table(title="Kokoro voice examples", header_style="listen.brand", expand=False)
        table.add_column("Language", style="listen.info", no_wrap=True)
        table.add_column("Voice IDs")
        for language, voices in examples:
            table.add_row(language, voices)
        self.console.print(table)
        self.muted("A voice prefix selects its language. Other Kokoro voice IDs may also work.")
