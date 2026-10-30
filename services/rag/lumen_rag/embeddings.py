"""Text embedders. Both produce L2-normalised 384-dimension vectors, matching
the `vector(384)` column, so they can be swapped without a migration (but a
switch requires re-indexing, since the vector spaces differ)."""

from __future__ import annotations

import hashlib
import math
import re
from collections import Counter
from typing import Protocol, Sequence

import numpy as np

DIM = 384

_TOKEN = re.compile(r"[a-z0-9]+")
_STOP = frozenset(
    "a an and are as at be by can do does for from has have how i if in is it its my of on or "
    "our so that the their them then there they this to was we what when where which who why will "
    "with you your".split()
)


class Embedder(Protocol):
    name: str

    def embed(self, texts: Sequence[str]) -> np.ndarray: ...


def tokens(text: str) -> list[str]:
    return [t for t in _TOKEN.findall(text.lower()) if t not in _STOP]


def _stem(t: str) -> str:
    # A light suffix stripper: enough to match "refunds"/"refunded"/"refund".
    for suf in ("ing", "ed", "es", "s"):
        if len(t) > len(suf) + 3 and t.endswith(suf):
            return t[: -len(suf)]
    return t


class HashingEmbedder:
    """Feature-hashed bag of stemmed words, word bigrams and character
    trigrams, with sublinear term frequency. Deterministic, dependency-free
    and fast; a sensible baseline that the retrieval eval measures against."""

    name = "hashing"

    def _features(self, text: str) -> Counter[str]:
        words = [_stem(t) for t in tokens(text)]
        feats: Counter[str] = Counter()
        for w in words:
            feats["w:" + w] += 1
            padded = f"#{w}#"
            for i in range(len(padded) - 2):
                feats["c:" + padded[i : i + 3]] += 0.25
        for a, b in zip(words, words[1:]):
            feats[f"b:{a}_{b}"] += 1
        return feats

    def embed(self, texts: Sequence[str]) -> np.ndarray:
        out = np.zeros((len(texts), DIM), dtype=np.float32)
        for row, text in enumerate(texts):
            for feat, tf in self._features(text).items():
                h = int.from_bytes(hashlib.blake2b(feat.encode(), digest_size=8).digest(), "little")
                sign = 1.0 if (h >> 63) & 1 else -1.0
                out[row, h % DIM] += sign * (1.0 + math.log(tf)) if tf >= 1 else sign * tf
            norm = np.linalg.norm(out[row])
            if norm > 0:
                out[row] /= norm
        return out


class FastEmbedder:
    """BAAI/bge-small-en-v1.5 through fastembed (ONNX, CPU)."""

    name = "fastembed"

    def __init__(self, model: str = "BAAI/bge-small-en-v1.5") -> None:
        from fastembed import TextEmbedding  # optional dependency

        self._model = TextEmbedding(model)

    def embed(self, texts: Sequence[str]) -> np.ndarray:
        vecs = np.array(list(self._model.embed(list(texts))), dtype=np.float32)
        return vecs / np.linalg.norm(vecs, axis=1, keepdims=True)


def make_embedder(kind: str) -> Embedder:
    if kind == "fastembed":
        return FastEmbedder()
    if kind == "hashing":
        return HashingEmbedder()
    raise ValueError(f"unknown embedder {kind!r}")
