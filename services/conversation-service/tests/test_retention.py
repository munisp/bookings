"""Data-retention enforcement unit tests (NDPA 2023 — docs/compliance/ndpa.md):
config parsing, the direct SQL delete function (batched, tenant-scoped,
database-clock cutoff), and the sweeper's per-tenant orchestration. Offline:
fake pool/connection, no Postgres."""

from __future__ import annotations

import sys
import uuid

import pytest

sys.path.insert(0, ".")

from app.config import Config, load  # noqa: E402
from app.db import Database  # noqa: E402
from app.retention import RetentionSweeper  # noqa: E402

pytestmark = pytest.mark.asyncio

TENANT_A = uuid.uuid4()
TENANT_B = uuid.uuid4()


# --------------------------------------------------------------------------
# Config
# --------------------------------------------------------------------------
class TestConfig:
    async def test_defaults(self, monkeypatch):
        for k in ("RETENTION_ENABLED", "RETENTION_DAYS",
                  "RETENTION_SWEEP_SECONDS", "RETENTION_BATCH_SIZE"):
            monkeypatch.delenv(k, raising=False)
        cfg = load()
        assert cfg.retention_enabled is True
        assert cfg.retention_days == 365  # platform default
        assert cfg.retention_sweep_seconds == 3600  # hourly
        assert cfg.retention_batch_size == 1000

    async def test_ndpa_profile_env(self, monkeypatch):
        # infra/privacy/ndpa-profile.env sets RETENTION_DAYS=180.
        monkeypatch.setenv("RETENTION_DAYS", "180")
        monkeypatch.setenv("RETENTION_SWEEP_SECONDS", "3600")
        monkeypatch.setenv("RETENTION_BATCH_SIZE", "500")
        cfg = load()
        assert cfg.retention_days == 180
        assert cfg.retention_batch_size == 500

    async def test_disable(self, monkeypatch):
        monkeypatch.setenv("RETENTION_ENABLED", "false")
        assert load().retention_enabled is False


# --------------------------------------------------------------------------
# Direct SQL function: Database.delete_turns_older_than
# --------------------------------------------------------------------------
class _FakeConn:
    """Records statements; fetchval returns queued batch-delete counts."""

    def __init__(self, counts: list[int]):
        self._counts = list(counts)
        self.executed: list[tuple[str, tuple]] = []
        self.tenant_set: str | None = None

    def transaction(self):
        conn = self

        class _Tx:
            async def __aenter__(self):
                return conn

            async def __aexit__(self, *exc):
                return False

        return _Tx()

    async def execute(self, sql: str, *params):
        self.executed.append((sql, params))
        if "set_config" in sql:
            self.tenant_set = params[0]

    async def fetchval(self, sql: str, *params):
        self.executed.append((sql, params))
        return self._counts.pop(0) if self._counts else 0


class _FakeAcquire:
    def __init__(self, conn):
        self._conn = conn

    async def __aenter__(self):
        return self._conn

    async def __aexit__(self, *exc):
        return False


class _FakePool:
    def __init__(self, conn):
        self._conn = conn

    def acquire(self):
        return _FakeAcquire(self._conn)


def _db_with_conn(conn: _FakeConn) -> Database:
    db = Database(Config())
    db._pool = _FakePool(conn)
    return db


class TestDeleteTurnsOlderThan:
    async def test_batched_delete_in_tenant_tx(self):
        # Two full batches (1000) then a partial (37) -> loop stops, total 2037.
        conn = _FakeConn([1000, 1000, 37])
        db = _db_with_conn(conn)
        total = await db.delete_turns_older_than(TENANT_A, 180, batch_size=1000)
        assert total == 2037
        # Tenant scope set via app.tenant_id (RLS).
        assert conn.tenant_set == str(TENANT_A)
        deletes = [p for sql, p in conn.executed if "DELETE FROM turns" in sql]
        assert len(deletes) == 3
        assert all(p == (180, 1000) for p in deletes)

    async def test_cutoff_uses_database_clock(self):
        # The age cutoff must be now() in SQL (fake-clock safe: no app-side
        # timestamp is computed or passed — only the day count is a param).
        conn = _FakeConn([0])
        db = _db_with_conn(conn)
        await db.delete_turns_older_than(TENANT_A, 365)
        delete_sql = next(sql for sql, _ in conn.executed if "DELETE FROM turns" in sql)
        assert "now() - ($1::int * INTERVAL '1 day')" in delete_sql

    async def test_nothing_old_returns_zero(self):
        conn = _FakeConn([0])
        db = _db_with_conn(conn)
        assert await db.delete_turns_older_than(TENANT_A, 180) == 0
        deletes = [sql for sql, _ in conn.executed if "DELETE FROM turns" in sql]
        assert len(deletes) == 1  # single batch, no loop spin


# --------------------------------------------------------------------------
# Sweeper orchestration
# --------------------------------------------------------------------------
class _StubDB:
    def __init__(self, tenants, deleted=None, fail_on=None):
        self._tenants = tenants
        self._deleted = deleted or {}
        self._fail_on = fail_on or set()
        self.calls: list[tuple[uuid.UUID, int, int]] = []

    async def list_tenant_ids(self):
        return list(self._tenants)

    async def delete_turns_older_than(self, tenant_id, days, batch_size):
        self.calls.append((tenant_id, days, batch_size))
        if tenant_id in self._fail_on:
            raise RuntimeError("db down")
        return self._deleted.get(tenant_id, 0)


class TestSweeper:
    def _cfg(self, **kw):
        base = dict(retention_days=180, retention_sweep_seconds=3600,
                    retention_batch_size=1000)
        base.update(kw)
        return Config(**base)

    async def test_sweeps_every_tenant_with_configured_window(self):
        stub = _StubDB([TENANT_A, TENANT_B], deleted={TENANT_A: 42, TENANT_B: 7})
        sweeper = RetentionSweeper(self._cfg(), stub)
        result = await sweeper.run_once()
        assert result == {str(TENANT_A): 42, str(TENANT_B): 7}
        assert stub.calls == [(TENANT_A, 180, 1000), (TENANT_B, 180, 1000)]

    async def test_zero_deletes_not_reported(self):
        stub = _StubDB([TENANT_A, TENANT_B], deleted={TENANT_B: 3})
        result = await RetentionSweeper(self._cfg(), stub).run_once()
        assert result == {str(TENANT_B): 3}

    async def test_tenant_failure_does_not_stop_others(self):
        stub = _StubDB([TENANT_A, TENANT_B], deleted={TENANT_B: 5},
                       fail_on={TENANT_A})
        result = await RetentionSweeper(self._cfg(), stub).run_once()
        assert result == {str(TENANT_B): 5}
        assert len(stub.calls) == 2  # B still swept after A failed

    async def test_nonpositive_window_refuses(self):
        stub = _StubDB([TENANT_A])
        result = await RetentionSweeper(self._cfg(retention_days=0), stub).run_once()
        assert result == {}
        assert stub.calls == []  # never deletes when misconfigured

    async def test_start_stop_lifecycle(self):
        stub = _StubDB([TENANT_A], deleted={TENANT_A: 1})
        sweeper = RetentionSweeper(self._cfg(retention_sweep_seconds=3600), stub)
        sweeper.start()
        import asyncio

        await asyncio.sleep(0.05)  # first sweep runs at startup
        await sweeper.stop()
        assert len(stub.calls) >= 1
