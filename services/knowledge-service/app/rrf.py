"""Reciprocal Rank Fusion — pure logic, no infra deps (unit-testable)."""

from __future__ import annotations

from typing import Any


def reciprocal_rank_fusion(
    rankings: list[list[dict[str, Any]]], *, rrf_k: int = 60, size: int = 10
) -> list[dict[str, Any]]:
    """Fuse ranked hit lists with RRF: score(d) = sum over lists of 1/(rrf_k + rank).

    `rank` is the 1-based position within each list. Ties break by best single
    list rank. Returns fused hits annotated with `_rrf_score`.
    """
    scores: dict[str, float] = {}
    best_rank: dict[str, int] = {}
    docs: dict[str, dict[str, Any]] = {}
    for hits in rankings:
        for rank, hit in enumerate(hits, start=1):
            doc_id = hit["_id"]
            scores[doc_id] = scores.get(doc_id, 0.0) + 1.0 / (rrf_k + rank)
            best_rank[doc_id] = min(best_rank.get(doc_id, rank), rank)
            docs.setdefault(doc_id, hit)
    ordered = sorted(scores, key=lambda d: (-scores[d], best_rank[d]))[:size]
    out = []
    for doc_id in ordered:
        hit = dict(docs[doc_id])
        hit["_rrf_score"] = scores[doc_id]
        out.append(hit)
    return out
