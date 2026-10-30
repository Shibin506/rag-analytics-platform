"""Split Markdown or plain text into retrieval chunks.

Chunks follow the document's structure: a chunk never mixes two sections, and
every chunk starts with its section path ("Billing and plans > Refunds"), so a
short paragraph still carries the context that makes it findable.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

_HEADING = re.compile(r"^(#{1,6})\s+(.*\S)\s*$")


@dataclass(frozen=True)
class Chunk:
    ordinal: int
    content: str


def _sections(text: str) -> list[tuple[str, list[str]]]:
    """Return (heading path, paragraphs) pairs in document order."""
    path: list[str] = []
    sections: list[tuple[str, list[str]]] = []
    paragraphs: list[str] = []
    current: list[str] = []

    def end_paragraph() -> None:
        if current:
            paragraphs.append(" ".join(s.strip() for s in current).strip())
            current.clear()

    def end_section() -> None:
        end_paragraph()
        if paragraphs:
            sections.append((" > ".join(path), paragraphs.copy()))
            paragraphs.clear()

    for line in text.splitlines():
        m = _HEADING.match(line)
        if m:
            end_section()
            level = len(m.group(1))
            del path[level - 1 :]
            path.extend([""] * (level - 1 - len(path)))
            path.append(m.group(2))
            path[:] = [p for p in path if p]
        elif not line.strip():
            end_paragraph()
        elif line.lstrip().startswith(("- ", "* ")) and current:
            # Keep list items as separate lines inside one paragraph.
            current.append("\n" + line.strip())
        else:
            current.append(line)
    end_section()
    return sections


def chunk_text(text: str, max_words: int = 120) -> list[Chunk]:
    """Pack paragraphs into chunks of at most ~max_words, per section.

    A paragraph longer than max_words is split on sentence boundaries.
    """
    chunks: list[Chunk] = []

    def emit(heading: str, parts: list[str]) -> None:
        body = "\n\n".join(parts).strip()
        if body:
            content = f"{heading}\n\n{body}" if heading else body
            chunks.append(Chunk(len(chunks), content))

    for heading, paragraphs in _sections(text):
        units: list[str] = []
        for p in paragraphs:
            if len(p.split()) <= max_words:
                units.append(p)
            else:
                units.extend(_split_sentences(p, max_words))
        buf: list[str] = []
        words = 0
        for u in units:
            n = len(u.split())
            if buf and words + n > max_words:
                emit(heading, buf)
                buf, words = [], 0
            buf.append(u)
            words += n
        emit(heading, buf)
    return chunks


def _split_sentences(paragraph: str, max_words: int) -> list[str]:
    sentences = re.split(r"(?<=[.!?])\s+", paragraph)
    out: list[str] = []
    buf: list[str] = []
    for s in sentences:
        if buf and len(" ".join(buf + [s]).split()) > max_words:
            out.append(" ".join(buf))
            buf = []
        buf.append(s)
    if buf:
        out.append(" ".join(buf))
    return out
