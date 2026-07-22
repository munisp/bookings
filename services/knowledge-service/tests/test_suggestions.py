"""Self-improving KB unit tests (SPEC-W3 §4, innovation 4): question
detection, the suggest gate and suggestion store functions with a fake conn."""

from __future__ import annotations

import sys
import uuid
from datetime import UTC, datetime

import pytest

sys.path.insert(0, ".")

from app import suggestions  # noqa: E402

TENANT = uuid.uuid4()


# ------------------------------------------------------------ question shape
@pytest.mark.parametrize(
    "q",
    [
        "Do you offer gift cards?",
        "do you open on Sundays",          # question word, no '?'
        "What is the cancellation policy",
        "How much does a balayage cost?",
        "Is parking available?",
        "Can I bring my child?",
        "  where are you located  ",
    ],
)
def test_looks_like_question_true(q):
    assert suggestions.looks_like_question(q) is True


@pytest.mark.parametrize(
    "q",
    [
        "gift cards",
        "opening hours",
        "",
        "   ",
        "123 main street",
    ],
)
def test_looks_like_question_false(q):
    assert suggestions.looks_like_question(q) is False


# ---------------------------------------------------------------- the gate
def test_should_suggest_below_threshold_question():
    assert suggestions.should_suggest(0.12, "Do you validate parking?", 0.35) is True


def test_should_suggest_no_hits():
    assert suggestions.should_suggest(None, "What forms of payment?", 0.35) is True


def test_should_suggest_above_threshold():
    assert suggestions.should_suggest(0.50, "Do you validate parking?", 0.35) is False


def test_should_suggest_not_a_question():
    assert suggestions.should_suggest(0.0, "parking validation", 0.35) is False


def test_should_suggest_threshold_boundary():
    assert suggestions.should_suggest(0.35, "How are you?", 0.35) is False


# ---------------------------------------------------------------- store fns
class FakeConn:
    """Records SQL; scriptable fetchrow/fetch/execute results."""

    def __init__(self, *, fetchrow_result=None, fetch_result=None, execute_result="UPDATE 1"):
        self.fetchrow_result = fetchrow_result
        self.fetch_result = fetch_result or []
        self.execute_result = execute_result
        self.calls = []

    async def fetchrow(self, query, *args):
        self.calls.append(("fetchrow", query, args))
        return self.fetchrow_result

    async def fetch(self, query, *args):
        self.calls.append(("fetch", query, args))
        return self.fetch_result

    async def execute(self, query, *args):
        self.calls.append(("execute", query, args))
        return self.execute_result


def _row(**overrides):
    row = dict(
        id=uuid.uuid4(),
        tenant_id=TENANT,
        question="Do you offer gift cards?",
        top_score=0.12,
        status="pending",
        created_at=datetime.now(UTC),
    )
    row.update(overrides)
    return row


async def test_record_suggestion_inserts_with_dedupe():
    row = _row()
    conn = FakeConn(fetchrow_result=row)
    result = await suggestions.record_suggestion(conn, TENANT, row["question"], 0.12)
    assert result == row
    kind, query, args = conn.calls[0]
    assert "ON CONFLICT (tenant_id, question) DO NOTHING" in query
    assert args == (TENANT, row["question"], 0.12)


async def test_record_suggestion_dedupes():
    conn = FakeConn(fetchrow_result=None)  # conflict -> no row returned
    result = await suggestions.record_suggestion(conn, TENANT, "Dupe?", 0.1)
    assert result is None


async def test_list_suggestions_all():
    rows = [_row(), _row()]
    conn = FakeConn(fetch_result=rows)
    result = await suggestions.list_suggestions(conn, TENANT)
    assert result == rows
    assert "WHERE status" not in conn.calls[0][1]


async def test_list_suggestions_filtered():
    conn = FakeConn(fetch_result=[_row()])
    result = await suggestions.list_suggestions(conn, TENANT, "pending")
    assert len(result) == 1
    kind, query, args = conn.calls[0]
    assert "WHERE status = $1" in query and args == ("pending",)


async def test_get_suggestion():
    row = _row()
    conn = FakeConn(fetchrow_result=row)
    assert await suggestions.get_suggestion(conn, row["id"]) == row
    conn2 = FakeConn(fetchrow_result=None)
    assert await suggestions.get_suggestion(conn2, uuid.uuid4()) is None


async def test_set_status():
    sid = uuid.uuid4()
    conn = FakeConn(execute_result="UPDATE 1")
    assert await suggestions.set_status(conn, sid, "approved") is True
    kind, query, args = conn.calls[0]
    assert args == (sid, "approved")

    conn2 = FakeConn(execute_result="UPDATE 0")
    assert await suggestions.set_status(conn2, sid, "rejected") is False


def test_ddl_is_idempotent():
    assert "CREATE TABLE IF NOT EXISTS kb_suggestions" in suggestions.DDL
    assert "UNIQUE (tenant_id, question)" in suggestions.DDL
    assert "FORCE ROW LEVEL SECURITY" in suggestions.DDL
    assert "IF NOT EXISTS" in suggestions.DDL  # policy creation guarded
