"""OpenSearch access: `kb-chunks` index management, bulk indexing, hybrid
BM25 + kNN search with client-side Reciprocal Rank Fusion (SPEC §10).
"""

from __future__ import annotations

from typing import Any

from opensearchpy import AsyncOpenSearch
from opensearchpy.helpers import async_bulk

from .logging import get_logger
from .rrf import reciprocal_rank_fusion  # noqa: F401  (re-exported)

log = get_logger(__name__)


# OpenSearch `kb-chunks` mapping per SPEC §10 (knn_vector 384-dim).
def index_body(dim: int) -> dict[str, Any]:
    return {
        "settings": {"index": {"knn": True, "number_of_shards": 1, "number_of_replicas": 0}},
        "mappings": {
            "properties": {
                "tenant_id": {"type": "keyword"},
                "document_id": {"type": "keyword"},
                "chunk_id": {"type": "keyword"},
                "seq": {"type": "integer"},
                "title": {"type": "text"},
                "content": {"type": "text"},
                "embedding": {
                    "type": "knn_vector",
                    "dimension": dim,
                    "method": {
                        "name": "hnsw",
                        "engine": "lucene",
                        "space_type": "cosinesimil",
                        "parameters": {"ef_construction": 128, "m": 24},
                    },
                },
            }
        },
    }


class SearchStore:
    def __init__(self, url: str, index: str, dim: int, username: str = "", password: str = ""):
        auth = (username, password) if username else None
        self._os = AsyncOpenSearch(
            hosts=[url],
            http_auth=auth,
            verify_certs=False,
            ssl_show_warn=False,
        )
        self._index = index
        self._dim = dim

    async def aclose(self) -> None:
        await self._os.close()

    async def ping(self) -> bool:
        try:
            return bool(await self._os.ping())
        except Exception:
            return False

    async def ensure_index(self) -> None:
        exists = await self._os.indices.exists(index=self._index)
        if not exists:
            await self._os.indices.create(index=self._index, body=index_body(self._dim))
            log.info("opensearch.index_created", index=self._index, dim=self._dim)

    async def bulk_index_chunks(self, docs: list[dict[str, Any]]) -> int:
        actions = [
            {"_index": self._index, "_id": d["chunk_id"], "_source": d} for d in docs
        ]
        if not actions:
            return 0
        ok, errors = await async_bulk(self._os, actions, raise_on_error=False)
        if errors:
            log.warning("opensearch.bulk_errors", count=len(errors))
        await self._os.indices.refresh(index=self._index)
        return int(ok)

    async def delete_document(self, tenant_id: str, document_id: str) -> None:
        await self._os.delete_by_query(
            index=self._index,
            body={
                "query": {
                    "bool": {
                        "filter": [
                            {"term": {"tenant_id": tenant_id}},
                            {"term": {"document_id": document_id}},
                        ]
                    }
                }
            },
            conflicts="proceed",
        )
        await self._os.indices.refresh(index=self._index)

    async def bm25_search(self, tenant_id: str, query: str, k: int) -> list[dict[str, Any]]:
        resp = await self._os.search(
            index=self._index,
            body={
                "size": k,
                "query": {
                    "bool": {
                        "must": {
                            "multi_match": {
                                "query": query,
                                "fields": ["content", "title^2"],
                                "type": "best_fields",
                            }
                        },
                        "filter": [{"term": {"tenant_id": tenant_id}}],
                    }
                },
            },
        )
        return resp["hits"]["hits"]

    async def knn_search(
        self, tenant_id: str, vector: list[float], k: int
    ) -> list[dict[str, Any]]:
        resp = await self._os.search(
            index=self._index,
            body={
                "size": k,
                "query": {
                    "knn": {
                        "embedding": {
                            "vector": vector,
                            "k": k,
                            "filter": {"term": {"tenant_id": tenant_id}},
                        }
                    }
                },
            },
        )
        return resp["hits"]["hits"]
