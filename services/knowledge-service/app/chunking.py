"""Word-based text chunker approximating ~500 tokens with overlap.

We deliberately avoid pulling a full BPE tokenizer: English prose averages
~0.75 words per token, so 375 words ~ 500 tokens (see config). Chunks split
on sentence boundaries when possible to keep embeddings coherent.
"""

from __future__ import annotations

import re

_SENTENCE_RE = re.compile(r"(?<=[.!?])\s+")


def split_sentences(text: str) -> list[str]:
    text = re.sub(r"\s+", " ", text).strip()
    if not text:
        return []
    return [s for s in _SENTENCE_RE.split(text) if s.strip()]


def chunk_text(text: str, *, chunk_words: int = 375, overlap_words: int = 64) -> list[str]:
    """Split `text` into chunks of at most `chunk_words` words.

    Overlap is achieved by rewinding `overlap_words` words after each chunk
    boundary. Long single sentences are hard-split on word count.
    """
    sentences = split_sentences(text)
    if not sentences:
        return []

    chunks: list[str] = []
    current: list[str] = []
    current_words = 0

    def flush() -> None:
        nonlocal current, current_words
        if current:
            chunks.append(" ".join(current).strip())
        current = []
        current_words = 0

    for sentence in sentences:
        words = sentence.split()
        if len(words) > chunk_words:
            # hard-split oversized sentence
            flush()
            for i in range(0, len(words), chunk_words - overlap_words):
                chunks.append(" ".join(words[i : i + chunk_words]))
            continue
        if current_words + len(words) > chunk_words and current:
            # close chunk, then seed next chunk with trailing overlap words
            chunk_text_joined = " ".join(current)
            chunks.append(chunk_text_joined.strip())
            tail = chunk_text_joined.split()[-overlap_words:]
            current = [" ".join(tail), sentence]
            current_words = len(tail) + len(words)
        else:
            current.append(sentence)
            current_words += len(words)
    flush()
    return [c for c in chunks if c]
