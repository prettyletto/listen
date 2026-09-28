"""Section detection and a content-checked JSON outline cache."""

from __future__ import annotations

import hashlib
import html
import json
import os
import re
import tempfile
from pathlib import Path
from typing import Any


OUTLINE_VERSION = 2
_FENCE_START = re.compile(r"^ {0,3}(`{3,}|~{3,})")
_ATX = re.compile(r"^ {0,3}(#{1,6})\s+(.+?)\s*#*\s*$")
_ASCIIDOC = re.compile(r"^={1,6}\s+(.+?)\s*$")
_ORG = re.compile(r"^(\*{1,6})\s+(.+?)\s*$")
_HTML_HEADING = re.compile(r"^\s*<h([1-6])\b[^>]*>(.*?)</h\1>\s*$", re.IGNORECASE)
_LATEX_HEADING = re.compile(r"^\s*\\(part|chapter|section|subsection|subsubsection)\*?(?:\[[^\]]*\])?\s*\{(.+?)\}(?:\s*\\label\{[^}]+\})?\s*$", re.IGNORECASE)
_PART_HEADING = re.compile(
    r"^(?P<kind>part|parte|book|livro|volume)\s+(?!(?:of|do|de|the|da)\b)(?P<identifier>[\w-]+)\b",
    re.IGNORECASE,
)
_NUMBERED = re.compile(r"^\s*(\d+(?:\.\d+){0,5})[.)]?\s+(.+?)\s*$")
_CHAPTER_PREFIX = re.compile(
    r"^\s*(?:chapter|chapitre|cap[ií]tulo|capitolo|kapitel|unit|unidade|lesson|"
    r"lecci[oó]n|li[cç][aã]o|aula|module|m[oó]dulo|modulo|session|week|day|"
    r"article|art[ií]culo|artigo)\b",
    re.IGNORECASE,
)
_NUMBERED_CHAPTER_TITLE = re.compile(
    r"^\s*(?:\d{1,3}|[IVXLCDM]{1,8})[.)-:]?\s+\S",
    re.IGNORECASE,
)
_BOOK_MATTER_TITLE = re.compile(
    r"^\s*(?:appendix|appendices|annex|annexe|glossary|bibliography|references|index|"
    r"closing note|afterword|epilogue|conclusion|acknowledg(?:e)?ments|foreword|"
    r"preface|prologue|introduction)\b",
    re.IGNORECASE,
)
_DECORATED = re.compile(r"^\s*(?:={3,}|-{3,})\s*(.{2,120}?)\s*(?:={3,}|-{3,})\s*$")
_SETEXT = re.compile(r"^ {0,3}(=+|-+|~+|\^+|\"{3,})\s*$")
_EXPLICIT = re.compile(
    r"^\s*(?P<kind>chapter|chapitre|cap[ií]tulo|capitolo|kapitel|section|subsection|"
    r"secci[oó]n|se[cç][aã]o|sezione|part|parte|book|livro|volume|unit|unidade|"
    r"lesson|lecci[oó]n|li[cç][aã]o|aula|module|m[oó]dulo|modulo|session|week|day|"
    r"topic|article|art[ií]culo|artigo|rule|appendix|ap[eê]ndice|pr[oó]logo|"
    r"prologue|epilogue|introduction|conclusion|preface|foreword)\b"
    r"(?:\s+(?P<identifier>(?:[\dIVXLCDM]+|[A-Z])(?:[.\d]*)?))?"
    r"(?:\s*[:.)-]?\s+(?P<title>\S.*))?\s*$",
    re.IGNORECASE,
)
_ASCII_BLOCKS = {"----", "....", "++++"}
_EXPLICIT_LEVEL = {
    "part": 1,
    "parte": 1,
    "book": 1,
    "livro": 1,
    "volume": 1,
    "prólogo": 1,
    "prologo": 1,
    "prologue": 1,
    "epilogue": 1,
    "introduction": 1,
    "conclusion": 1,
    "preface": 1,
    "foreword": 1,
    "chapter": 2,
    "chapitre": 2,
    "capítulo": 2,
    "capitulo": 2,
    "capitolo": 2,
    "kapitel": 2,
    "unit": 2,
    "unidade": 2,
    "lesson": 2,
    "lección": 2,
    "leccion": 2,
    "lição": 2,
    "licao": 2,
    "aula": 2,
    "module": 2,
    "módulo": 2,
    "modulo": 2,
    "session": 2,
    "week": 2,
    "day": 2,
    "section": 3,
    "sección": 3,
    "seccion": 3,
    "seção": 3,
    "secao": 3,
    "sezione": 3,
    "subsection": 4,
    "topic": 3,
    "article": 2,
    "artículo": 2,
    "articulo": 2,
    "artigo": 2,
    "rule": 2,
    "appendix": 2,
    "apéndice": 2,
    "apendice": 2,
}


def is_part_title(title: str) -> bool:
    """Whether a heading names a book/part/volume grouping."""
    return _PART_HEADING.match(title) is not None


def part_label(title: str) -> str:
    """Return the short grouping label used beside promoted chapters."""
    match = _PART_HEADING.match(title)
    if match is None:
        return title
    return f"{match.group('kind').title()} {match.group('identifier')}"


def is_chapter_title(title: str) -> bool:
    """Recognize common explicit and numbered chapter heading forms."""
    return _CHAPTER_PREFIX.match(title) is not None or _NUMBERED_CHAPTER_TITLE.match(title) is not None


def is_book_matter_title(title: str) -> bool:
    """Whether a heading names front/back matter rather than a numbered chapter."""
    return _BOOK_MATTER_TITLE.match(title) is not None


def _cache_root() -> Path:
    xdg_cache = os.environ.get("XDG_CACHE_HOME")
    cache_home = Path(xdg_cache).expanduser() if xdg_cache else Path.home() / ".cache"
    if not cache_home.is_absolute():
        cache_home = Path.home() / cache_home
    return cache_home / "listen-cli" / "outlines"


def outline_cache_path(source: Path) -> Path:
    """Return the stable outline-cache path for a source file."""
    canonical = str(source.resolve(strict=False))
    key = hashlib.sha256(canonical.encode("utf-8")).hexdigest()
    return _cache_root() / f"{key}.json"


def _fenced_line_state(line: str, fence: tuple[str, int] | str | None) -> tuple[tuple[str, int] | str | None, bool]:
    """Update fenced-code state and report whether this line is fenced."""
    if isinstance(fence, tuple):
        closing = re.match(r"^ {0,3}(`+|~+)\s*$", line)
        if closing and closing.group(1)[0] == fence[0] and len(closing.group(1)) >= fence[1]:
            return None, True
        return fence, True
    if isinstance(fence, str):
        if fence == "org-src":
            if re.match(r"^\s*#\+END_(?:SRC|EXAMPLE)\b", line, re.IGNORECASE):
                return None, True
            return fence, True
        if line.strip() == fence:
            return None, True
        return fence, True

    opening = _FENCE_START.match(line)
    if opening:
        marker = opening.group(1)
        return (marker[0], len(marker)), True
    if re.match(r"^\s*#\+BEGIN_(?:SRC|EXAMPLE)\b", line, re.IGNORECASE):
        return "org-src", True
    stripped = line.strip()
    if stripped in _ASCII_BLOCKS:
        return stripped, True
    return None, False


def _is_setext_title(line: str, previous_line: str | None) -> bool:
    title = line.strip()
    if not title or len(title) > 160 or _SETEXT.fullmatch(title):
        return False
    if title.startswith(("#", ">", "- ", "* ", "+ ")):
        return False
    if title.endswith((".", "?", "!", ";", ",")):
        return False
    return previous_line is None or not previous_line.strip()


def _explicit_heading(line: str) -> tuple[str, int] | None:
    if line.strip().endswith((".", "?", "!", ";", ",")):
        return None
    match = _EXPLICIT.match(line)
    if not match:
        return None
    title = " ".join(part for part in (match.group("kind"), match.group("identifier"), match.group("title")) if part)
    if len(title) > 180:
        return None
    kind = match.group("kind").casefold()
    return title.rstrip(" :.-"), _EXPLICIT_LEVEL.get(kind, 2)


def _plain_heading(line: str, previous_line: str | None, next_line: str | None) -> tuple[str, int] | None:
    decorated = _DECORATED.match(line)
    if decorated:
        return decorated.group(1).strip(), 1

    atx = _ATX.match(line)
    if atx:
        return atx.group(2).strip(), len(atx.group(1))

    asciidoc = _ASCIIDOC.match(line)
    if asciidoc:
        # AsciiDoc document titles begin with one '='; the remaining levels
        # retain their natural nesting beneath that title.
        return asciidoc.group(1).strip(), len(re.match(r"^={1,6}", line).group(0))

    org = _ORG.match(line)
    if org:
        return org.group(2).strip(), len(org.group(1))

    html_heading = _HTML_HEADING.match(line)
    if html_heading:
        title = html.unescape(re.sub(r"<[^>]+>", "", html_heading.group(2))).strip()
        return title, int(html_heading.group(1))

    latex_heading = _LATEX_HEADING.match(line)
    if latex_heading:
        title = latex_heading.group(2).strip()
        level = {"part": 1, "chapter": 2, "section": 3, "subsection": 4, "subsubsection": 5}
        return title, level[latex_heading.group(1).casefold()]

    explicit = _explicit_heading(line)
    if explicit:
        return explicit

    numbered = _NUMBERED.match(line)
    if numbered:
        title = numbered.group(2).strip()
        standalone = (previous_line is None or not previous_line.strip()) and (next_line is None or not next_line.strip())
        if len(title) <= 160 and not title.endswith((".", "?", "!", ";", ",")) and ("." in numbered.group(1) or standalone):
            return f"{numbered.group(1)} {title}", numbered.group(1).count(".") + 1

    # Standalone uppercase titles are common in TXT books and exported notes.
    # Keep the heuristic strict enough to avoid treating ordinary prose as a
    # heading: it must be short, isolated, and contain multiple words (or a
    # recognizable title numeral).
    title = line.strip().strip("*_~=- ")
    letters = [character for character in title if character.isalpha()]
    words = re.findall(r"\b\w+\b", title, flags=re.UNICODE)
    isolated = (previous_line is None or not previous_line.strip()) and (next_line is None or not next_line.strip())
    uppercase = bool(letters) and len(title) <= 100 and title == title.upper()
    if isolated and uppercase and (len(words) >= 2 or len(title) >= 8):
        return title, 1

    title_case = title.istitle() and 4 <= len(title) <= 80 and len(words) <= 10
    if isolated and title_case and not title.endswith((".", "?", "!", ";", ",", ":")):
        return title, 3

    return None


def parse_sections(source: str) -> list[dict[str, Any]]:
    """Find likely structural headings and return their inclusive line spans.

    Recognizes Markdown, Setext/reStructuredText, AsciiDoc, Org, numbered and
    named chapter headings, and isolated uppercase headings in plain text.
    Lines inside common code fences are ignored.
    """
    lines = source.splitlines()
    candidates: list[dict[str, Any]] = []
    fence: tuple[str, int] | str | None = None
    index = 0

    while index < len(lines):
        line = lines[index]
        fence, inside_fence = _fenced_line_state(line, fence)
        if inside_fence or line.startswith(("    ", "\t")):
            index += 1
            continue

        # Underlined Markdown and reStructuredText titles.
        if index + 1 < len(lines):
            underline = _SETEXT.fullmatch(lines[index + 1])
            previous = lines[index - 1] if index else None
            if underline and _is_setext_title(line, previous):
                marker = underline.group(1)[0]
                level = {"=": 1, "-": 2, "~": 3, "^": 4, '"': 5}[marker]
                candidates.append({"title": line.strip(), "level": level, "line_index": index})
                index += 2
                continue

        heading = _plain_heading(line, lines[index - 1] if index else None, lines[index + 1] if index + 1 < len(lines) else None)
        if heading:
            title, level = heading
            if title and len(title) <= 180:
                candidates.append({"title": title, "level": max(1, min(6, level)), "line_index": index})

        index += 1

    # A Part/Book heading is a parent in plain text books too. Some authors
    # number each chapter from 1 again under each part, so preserve one level
    # of nesting for every heading until the next Part/Book heading.
    inside_part = False
    for candidate in candidates:
        if _PART_HEADING.match(candidate["title"]):
            inside_part = True
            continue
        if inside_part:
            candidate["level"] = min(6, candidate["level"] + 1)

    sections: list[dict[str, Any]] = []
    for position, candidate in enumerate(candidates):
        next_start = len(lines)
        for following in candidates[position + 1 :]:
            if following["level"] <= candidate["level"]:
                next_start = following["line_index"]
                break
        sections.append(
            {
                "title": candidate["title"],
                "level": candidate["level"],
                "start_line": candidate["line_index"] + 1,
                # 1-based inclusive end; also the exclusive Python slice end.
                "end_line": next_start,
            }
        )
    return sections


def _valid_outline(value: Any, source_path: str, source_sha256: str, line_count: int) -> bool:
    if not isinstance(value, dict):
        return False
    if value.get("outline_version") != OUTLINE_VERSION:
        return False
    if value.get("source_path") != source_path or value.get("source_sha256") != source_sha256:
        return False
    sections = value.get("sections")
    if not isinstance(sections, list):
        return False
    for section in sections:
        if not isinstance(section, dict) or not isinstance(section.get("title"), str):
            return False
        if not isinstance(section.get("level"), int):
            return False
        start, end = section.get("start_line"), section.get("end_line")
        if not isinstance(start, int) or not isinstance(end, int) or start < 1 or end < start or end > line_count:
            return False
    return True


def _write_outline(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path: Path | None = None
    try:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, prefix=f".{path.stem}-", suffix=".tmp", delete=False) as temporary:
            temporary_path = Path(temporary.name)
            json.dump(payload, temporary, ensure_ascii=False, indent=2)
            temporary.write("\n")
            temporary.flush()
            os.fsync(temporary.fileno())
        os.replace(temporary_path, path)
    finally:
        if temporary_path is not None:
            temporary_path.unlink(missing_ok=True)


def get_outline(source_path: Path, source_text: str, source_sha256: str) -> tuple[list[dict[str, Any]], bool, Path, bool]:
    """Read a matching JSON outline or parse and cache a fresh one.

    Returns (sections, cache_hit, cache_path, cache_saved). Cache failures are
    non-fatal; section selection and narration still work without a writable
    cache.
    """
    canonical_path = str(source_path.resolve(strict=False))
    cache_path = outline_cache_path(source_path)
    line_count = len(source_text.splitlines())
    try:
        value = json.loads(cache_path.read_text(encoding="utf-8"))
        if _valid_outline(value, canonical_path, source_sha256, line_count):
            return value["sections"], True, cache_path, True
    except (OSError, ValueError, TypeError):
        pass

    sections = parse_sections(source_text)
    payload = {
        "outline_version": OUTLINE_VERSION,
        "source_path": canonical_path,
        "source_sha256": source_sha256,
        "sections": sections,
    }
    cache_saved = False
    try:
        _write_outline(cache_path, payload)
        cache_saved = True
    except OSError:
        # A read-only cache should not prevent one-shot use of the CLI.
        pass
    return sections, False, cache_path, cache_saved
