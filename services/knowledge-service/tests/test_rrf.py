"""Smoke tests for reciprocal rank fusion."""

from app.rrf import reciprocal_rank_fusion


def hit(doc_id):
    return {"_id": doc_id, "_source": {"content": f"doc {doc_id}"}}


def test_rrf_prefers_docs_in_both_lists():
    bm25 = [hit("a"), hit("b"), hit("c")]
    knn = [hit("b"), hit("a"), hit("d")]
    fused = reciprocal_rank_fusion([bm25, knn], rrf_k=60, size=4)
    ids = [h["_id"] for h in fused]
    # a and b appear in both lists → must outrank c and d
    assert set(ids[:2]) == {"a", "b"}
    assert ids.index("b") < ids.index("d")
    for h in fused:
        assert h["_rrf_score"] > 0


def test_rrf_size_limit():
    fused = reciprocal_rank_fusion([[hit(str(i)) for i in range(20)]], size=5)
    assert len(fused) == 5
