"""Answer a question from retrieved excerpts, citing them as [S1], [S2]...

Two answerers share one interface:
- ClaudeAnswerer streams a grounded answer from Claude.
- ExtractiveAnswerer picks the most relevant sentences itself; it needs no API
  key, so the platform (and CI) work without one.
"""

from __future__ import annotations

import html
import re
from dataclasses import dataclass
from typing import AsyncIterator, Protocol, Sequence

import anthropic

from .embeddings import _stem, tokens
from .retrieval import Hit

NOT_FOUND = "I couldn't find this in your documents."

SYSTEM_PROMPT = """You answer questions for a company's support and analytics team using only the excerpts from their own documents provided in <source> tags.

Rules:
- Use only facts stated in the sources. If they do not contain the answer, reply exactly: "I couldn't find this in your documents."
- Cite every sentence that states a fact with the id of the source it came from, like [S2]. Use several ids when several sources support a sentence, like [S1][S3].
- Be brief: two to five sentences, plain prose, no headings. Quote numbers, limits and names exactly as the sources state them."""


@dataclass
class Answer:
    text: str
    mode: str
    cited: list[int]  # chunk ids actually cited


class Answerer(Protocol):
    mode: str

    def stream(self, question: str, hits: Sequence[Hit]) -> AsyncIterator[str]: ...


def format_sources(hits: Sequence[Hit]) -> str:
    return "\n".join(
        f'<source id="S{i}" title="{html.escape(h.title, quote=True)}">\n{h.content}\n</source>'
        for i, h in enumerate(hits, start=1)
    )


_CITE = re.compile(r"\[S(\d+)\]")


def cited_chunks(text: str, hits: Sequence[Hit]) -> list[int]:
    seen: list[int] = []
    for n in _CITE.findall(text):
        i = int(n) - 1
        if 0 <= i < len(hits) and hits[i].chunk_id not in seen:
            seen.append(hits[i].chunk_id)
    return seen


class ClaudeAnswerer:
    mode = "claude"

    def __init__(self, model: str, effort: str, client: anthropic.AsyncAnthropic | None = None) -> None:
        self.model = model
        self.effort = effort
        self.client = client or anthropic.AsyncAnthropic()

    async def stream(self, question: str, hits: Sequence[Hit]) -> AsyncIterator[str]:
        if not hits:
            yield NOT_FOUND
            return
        # fallbacks="default": if a safety classifier declines, the API re-runs
        # the request on Anthropic's recommended fallback model server-side.
        async with self.client.beta.messages.stream(
            model=self.model,
            max_tokens=4000,
            system=SYSTEM_PROMPT,
            output_config={"effort": self.effort},
            betas=["server-side-fallback-2026-07-01"],
            fallbacks="default",
            messages=[{
                "role": "user",
                "content": f"{format_sources(hits)}\n\nQuestion: {question}",
            }],
        ) as stream:
            async for text in stream.text_stream:
                yield text
            final = await stream.get_final_message()
        if final.stop_reason == "refusal":
            yield "\n\n(The model declined to answer this question.)"
        elif final.stop_reason == "max_tokens":
            yield " …"


_SENTENCE = re.compile(r"(?<=[.!?])\s+(?=[A-Z0-9*`\"(])")


class ExtractiveAnswerer:
    """Scores each sentence of the top excerpts by overlap with the question's
    stemmed terms (weighted by retrieval rank) and returns the best two or
    three, each cited."""

    mode = "extractive"

    def __init__(self, max_sentences: int = 3) -> None:
        self.max_sentences = max_sentences

    def compose(self, question: str, hits: Sequence[Hit]) -> str:
        q = {_stem(t) for t in tokens(question)}
        if not hits or not q:
            return NOT_FOUND
        scored: list[tuple[float, int, int, str]] = []
        for hi, h in enumerate(hits[:4]):
            body = h.content.split("\n\n", 1)[-1]  # drop the heading path
            for si, raw in enumerate(_SENTENCE.split(body.replace("\n", " "))):
                s = raw.strip().lstrip("-* ").strip()
                if len(s.split()) < 4:
                    continue
                terms = {_stem(t) for t in tokens(s)}
                overlap = len(q & terms) / len(q)
                if overlap == 0:
                    continue
                scored.append((overlap / (1 + 0.35 * hi), hi, si, s))
        if not scored:
            return NOT_FOUND
        best = sorted(scored, key=lambda x: -x[0])[: self.max_sentences]
        if best[0][0] < 0.2:
            return NOT_FOUND
        best.sort(key=lambda x: (x[1], x[2]))  # keep document order
        return " ".join(f"{s} [S{hi + 1}]" for _, hi, _, s in best)

    async def stream(self, question: str, hits: Sequence[Hit]) -> AsyncIterator[str]:
        yield self.compose(question, hits)
