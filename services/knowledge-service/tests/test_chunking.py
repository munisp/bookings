"""Smoke tests for the chunker (no model/infra needed)."""

from app.chunking import chunk_text, split_sentences


def test_empty_text_yields_no_chunks():
    assert chunk_text("") == []
    assert chunk_text("   \n  ") == []


def test_short_text_single_chunk():
    chunks = chunk_text("Hello world. This is a test.", chunk_words=50, overlap_words=5)
    assert chunks == ["Hello world. This is a test."]


def test_chunk_size_and_overlap():
    body = " ".join(f"w{i}" for i in range(300)) + "."
    chunks = chunk_text(body, chunk_words=100, overlap_words=20)
    assert len(chunks) > 1
    for c in chunks:
        assert len(c.split()) <= 100 + 1  # overlap seeding may add a join
    # overlap: second chunk starts with the tail of the first
    first_tail = chunks[0].split()[-20:]
    second_head = chunks[1].split()[:20]
    assert first_tail == second_head


def test_sentence_split():
    assert split_sentences("One. Two? Three!") == ["One.", "Two?", "Three!"]
