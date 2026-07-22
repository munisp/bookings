"""sentence-transformers embedding wrapper with lazy model load.

Model download happens once (from HF Hub) unless the cache is pre-populated
— the Docker image pre-downloads at build time (see Dockerfile/README).
"""

from __future__ import annotations

import asyncio
import os
from typing import Sequence

from .logging import get_logger

log = get_logger(__name__)


class Embedder:
    def __init__(self, model_name: str, cache_dir: str | None = None):
        self._model_name = model_name
        self._cache_dir = cache_dir
        self._model = None
        self._lock = asyncio.Lock()

    def _load(self):  # blocking — run in a thread
        from sentence_transformers import SentenceTransformer

        if self._cache_dir:
            os.environ.setdefault("SENTENCE_TRANSFORMERS_HOME", self._cache_dir)
        log.info("embedding_model.loading", model=self._model_name)
        return SentenceTransformer(self._model_name, device="cpu")

    async def ensure_loaded(self) -> None:
        if self._model is not None:
            return
        async with self._lock:
            if self._model is None:
                self._model = await asyncio.to_thread(self._load)
                log.info("embedding_model.loaded", model=self._model_name)

    async def embed(self, texts: Sequence[str]) -> list[list[float]]:
        """Embed texts; L2-normalized so cosinesimil == dot product."""
        await self.ensure_loaded()
        vectors = await asyncio.to_thread(
            self._model.encode,
            list(texts),
            normalize_embeddings=True,
            show_progress_bar=False,
        )
        return [v.tolist() for v in vectors]
