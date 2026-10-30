"""Settings, read once from the environment."""

from __future__ import annotations

import os
from dataclasses import dataclass, field


def _env(key: str, default: str) -> str:
    return os.environ.get(key) or default


@dataclass(frozen=True)
class Settings:
    database_url: str = field(default_factory=lambda: _env("DATABASE_URL", "postgresql://lumen:lumen@localhost:5432/lumen"))
    s3_endpoint: str = field(default_factory=lambda: _env("S3_ENDPOINT_URL", "http://localhost:8333"))
    s3_access_key: str = field(default_factory=lambda: _env("S3_ACCESS_KEY", "lumen"))
    s3_secret_key: str = field(default_factory=lambda: _env("S3_SECRET_KEY", "lumen-secret"))
    s3_bucket: str = field(default_factory=lambda: _env("S3_BUCKET", "lumen-lake"))
    s3_region: str = field(default_factory=lambda: _env("S3_REGION", "us-east-1"))

    # "hashing" needs no download and is deterministic (CI, demos);
    # "fastembed" uses BAAI/bge-small-en-v1.5 for better semantic recall.
    embedder: str = field(default_factory=lambda: _env("EMBEDDER", "hashing"))

    # Answers come from Claude when a key is configured, otherwise from the
    # extractive answerer, so the stack works without any API key.
    claude_model: str = field(default_factory=lambda: _env("CLAUDE_MODEL", "claude-opus-5-5"))
    claude_effort: str = field(default_factory=lambda: _env("CLAUDE_EFFORT", "low"))
    use_claude: bool = field(
        default_factory=lambda: _env("ANSWER_MODE", "auto") == "claude"
        or (_env("ANSWER_MODE", "auto") == "auto" and bool(os.environ.get("ANTHROPIC_API_KEY")))
    )

    top_k: int = field(default_factory=lambda: int(_env("RAG_TOP_K", "6")))
    chunk_words: int = field(default_factory=lambda: int(_env("RAG_CHUNK_WORDS", "120")))
    poll_seconds: float = field(default_factory=lambda: float(_env("RAG_POLL_SECONDS", "10")))
