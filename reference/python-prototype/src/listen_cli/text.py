"""Markdown cleanup and size-limited text chunking."""

from __future__ import annotations

import html
import re
import textwrap


_FENCE_START = re.compile(r"^ {0,3}(`{3,}|~{3,})")
_HEADING = re.compile(r"^ {0,3}#{1,6}\s+(.+?)\s*#*\s*$")
_ASCIIDOC_HEADING = re.compile(r"^={1,6}\s+(.+?)\s*$")
_ORG_HEADING = re.compile(r"^(\*{1,6})\s+(.+?)\s*$")
_HTML_HEADING = re.compile(r"^\s*<h[1-6]\b[^>]*>(.*?)</h[1-6]>\s*$", re.IGNORECASE)
_LATEX_HEADING = re.compile(r"^\s*\\(?:part|chapter|section|subsection|subsubsection)\*?(?:\[[^\]]*\])?\s*\{(.+?)\}(?:\s*\\label\{[^}]+\})?\s*$", re.IGNORECASE)
_SETEXT_UNDERLINE = re.compile(r"^ {0,3}(?:=+|-+|~+|\^+|\"{3,})\s*$")
_TABLE_RULE = re.compile(r"^:?-{3,}:?$")
_SENTENCE_BREAK = re.compile(r"(?<=[.!?])\s+")


def _inline_text(line: str) -> str:
    """Remove common inline Markdown while keeping text meant to be spoken."""
    line = re.sub(r"<!--.*?-->", " ", line)
    line = re.sub(r"\\(?:part|chapter|section|subsection|subsubsection)\*?(?:\[[^\]]*\])?\s*\{([^}]*)\}", r"\1", line, flags=re.IGNORECASE)
    line = re.sub(r"\\label\{[^}]+\}", "", line)
    line = re.sub(r"!\[([^\]]*)\]\([^)]*\)", " ", line)  # Ignore images.
    line = re.sub(r"!?\[([^\]]+)\]\([^)]*\)", r"\1", line)
    line = re.sub(r"\[([^\]]+)\]\[[^\]]*\]", r"\1", line)
    line = re.sub(r"\[\^[^\]]+\]", "", line)
    line = re.sub(r"`+([^`]+)`+", r"\1", line)
    line = re.sub(r"<[^>]+>", " ", line)
    line = re.sub(r"(?<!\w)[*_]{1,3}|[*_]{1,3}(?!\w)", "", line)
    line = re.sub(r"~~(.*?)~~", r"\1", line)
    line = html.unescape(line)
    return re.sub(r"\s+", " ", line).strip()


def markdown_to_speech(source: str, *, include_code: bool = False, source_format: str | None = None) -> str:
    """Turn common markup and plain text into clean prose for narration."""
    result: list[str] = []
    fence: tuple[str, int] | str | None = None
    asciidoc_format = source_format in {".adoc", ".asciidoc"}
    lines = source.splitlines()
    index = 0

    while index < len(lines):
        line = lines[index].rstrip()

        if isinstance(fence, str):
            if fence == "org-src" and re.match(r"^\s*#\+END_(?:SRC|EXAMPLE)\b", line, re.IGNORECASE):
                fence = None
            elif fence != "org-src" and line.strip() == fence:
                fence = None
            elif include_code:
                result.append(line.strip())
            index += 1
            continue

        if fence is not None:
            closing = re.match(r"^ {0,3}(`+|~+)\s*$", line)
            if closing and closing.group(1)[0] == fence[0] and len(closing.group(1)) >= fence[1]:
                fence = None
            elif include_code:
                result.append(line.strip())
            index += 1
            continue

        opening = _FENCE_START.match(line)
        if opening:
            marker = opening.group(1)
            fence = (marker[0], len(marker))
            index += 1
            continue

        if re.match(r"^\s*#\+BEGIN_(?:SRC|EXAMPLE)\b", line, re.IGNORECASE):
            fence = "org-src"
            index += 1
            continue

        if asciidoc_format and line.strip() in {"----", "....", "++++"}:
            fence = line.strip()
            index += 1
            continue
        if asciidoc_format and line.strip() == "====":
            result.append("")
            index += 1
            continue

        # Markdown Setext and reStructuredText titles put the heading marker
        # on a separate line. Treat the pair as one spoken heading.
        previous_line = lines[index - 1] if index else ""
        if (
            index + 1 < len(lines)
            and _SETEXT_UNDERLINE.fullmatch(lines[index + 1])
            and line.strip()
            and not previous_line.strip()
            and len(line.strip()) <= 160
            and not line.strip().endswith((".", "?", "!", ";", ","))
        ):
            spoken_heading = _inline_text(line.strip())
            if spoken_heading and spoken_heading[-1] not in ".!?…:;":
                spoken_heading += "."
            result.extend((spoken_heading, ""))
            index += 2
            continue

        if not include_code and (line.startswith("    ") or line.startswith("\t")):
            index += 1
            continue
        if re.match(r"^\s*\[\^[^\]]+\]:", line) or re.match(r"^\s*\[[^\]]+\]:\s*\S+", line):
            index += 1
            continue
        if re.fullmatch(r"\s*(?:[-*_]\s*){3,}", line):
            result.append("")
            index += 1
            continue

        heading = _HEADING.match(line)
        title = heading.group(1) if heading else None
        if title is None:
            heading = _ASCIIDOC_HEADING.match(line)
            title = heading.group(1) if heading else None
        if title is None:
            heading = _ORG_HEADING.match(line)
            title = heading.group(2) if heading else None
        if title is None:
            heading = _HTML_HEADING.match(line)
            title = heading.group(1) if heading else None
        if title is None:
            heading = _LATEX_HEADING.match(line)
            title = heading.group(1) if heading else None
        if title is not None:
            spoken_heading = _inline_text(title)
            if spoken_heading and spoken_heading[-1] not in ".!?…:;":
                spoken_heading += "."
            result.append(spoken_heading)
            result.append("")
            index += 1
            continue

        # Markdown tables are narrated as their cell values, without pipes or
        # alignment rows.
        table_row = "|" in line and line.strip().startswith("|")
        if table_row:
            cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
            if cells and all(_TABLE_RULE.fullmatch(cell.replace(" ", "")) for cell in cells):
                index += 1
                continue
            line = ". ".join(cell for cell in cells if cell)

        line = re.sub(r"^\s*>\s?", "", line)
        if not table_row:
            line = re.sub(r"^\s*(?:[-+*]|\d+[.)])\s+", "", line)
        spoken = _inline_text(line)
        result.append(spoken)
        index += 1

    cleaned_lines: list[str] = []
    for line in result:
        if line:
            cleaned_lines.append(line)
        elif cleaned_lines and cleaned_lines[-1] != "":
            cleaned_lines.append("")

    return "\n".join(cleaned_lines).strip()


def split_into_chunks(text: str, max_chars: int = 1600) -> list[str]:
    """Split text on paragraph and sentence boundaries for resumable TTS."""
    paragraphs = [part.strip().replace("\n", " ") for part in re.split(r"\n\s*\n", text) if part.strip()]
    pieces: list[str] = []

    for paragraph in paragraphs:
        sentences = [sentence.strip() for sentence in _SENTENCE_BREAK.split(paragraph) if sentence.strip()]
        if not sentences:
            continue

        bounded: list[str] = []
        for sentence in sentences:
            if len(sentence) <= max_chars:
                bounded.append(sentence)
                continue
            wrapped = textwrap.wrap(sentence, width=max_chars, break_long_words=False, break_on_hyphens=False)
            for part in wrapped:
                if len(part) <= max_chars:
                    bounded.append(part)
                else:
                    bounded.extend(textwrap.wrap(part, width=max_chars, break_long_words=True, break_on_hyphens=False))

        current = ""
        for sentence in bounded:
            candidate = f"{current}\n{sentence}" if current else sentence
            if current and len(candidate) > max_chars:
                pieces.append(current)
                current = sentence
            else:
                current = candidate
        if current:
            pieces.append(current)

    return pieces
