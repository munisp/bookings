package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// extra outbox rows (usage metering, Wave 5 #9) join the booking mutation
// transaction: both rows land atomically, or neither does.

func TestCreateBookingTxWritesExtraOutboxAtomically(t *testing.T) {
	tx := &fakeTx{}
	s := testStoreWithTx(tx)
	ctx := context.Background()

	b := &Booking{
		TenantID: uuid.New(), OfferingID: uuid.New(), TeamMemberID: uuid.New(),
		ContactID: uuid.New(), StartsAt: time.Now().Add(time.Hour), EndsAt: time.Now().Add(2 * time.Hour),
		Status: StatusPending, Source: "web",
	}
	usagePayload, _ := json.Marshal(map[string]any{"metric": "booking", "value": 1})
	err := s.CreateBookingTx(ctx, b, "opendesk.booking.events", []byte(`{"type":"BookingCreated"}`),
		ExtraOutbox{Topic: "opendesk.usage.events", Payload: usagePayload})
	if err != nil {
		t.Fatal(err)
	}

	var outboxInserts []string
	for _, q := range tx.queries {
		if q.kind == "exec" && containsStr(q.sql, "INSERT INTO outbox") {
			outboxInserts = append(outboxInserts, q.sql)
		}
	}
	if len(outboxInserts) != 2 {
		t.Fatalf("expected 2 outbox inserts (event + usage), got %d: %v", len(outboxInserts), tx.queries)
	}
	if !tx.committed || tx.rolledBack {
		t.Fatalf("tx must commit exactly once: committed=%v rolledBack=%v", tx.committed, tx.rolledBack)
	}
}

func TestSetBookingStatusWritesExtraOutbox(t *testing.T) {
	tx := &fakeTx{execRowsAffected: 1}
	s := testStoreWithTx(tx)
	ctx := context.Background()

	err := s.SetBookingStatus(ctx, uuid.New(), uuid.New(), StatusConfirmed,
		"opendesk.booking.events", []byte(`{"type":"BookingConfirmed"}`),
		ExtraOutbox{Topic: "opendesk.usage.events", Payload: []byte(`{"metric":"booking"}`)})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, q := range tx.queries {
		if q.kind == "exec" && containsStr(q.sql, "INSERT INTO outbox") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("expected 2 outbox inserts, got %d", count)
	}
}

func TestSetBookingStatusSkipsEmptyExtra(t *testing.T) {
	tx := &fakeTx{execRowsAffected: 1}
	s := testStoreWithTx(tx)
	err := s.SetBookingStatus(context.Background(), uuid.New(), uuid.New(), StatusConfirmed,
		"opendesk.booking.events", []byte(`{}`),
		ExtraOutbox{Topic: "", Payload: []byte(`x`)}, // skipped: no topic
		ExtraOutbox{Topic: "opendesk.usage.events", Payload: nil}) // skipped: no payload
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, q := range tx.queries {
		if q.kind == "exec" && containsStr(q.sql, "INSERT INTO outbox") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected only the primary outbox insert, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// minimal pgx.Tx fake (SQL-level verification without a database)
// ---------------------------------------------------------------------------

type recordedQuery struct {
	kind string // exec | queryrow
	sql  string
}

type fakeTx struct {
	pgx.Tx
	queries          []recordedQuery
	execRowsAffected int64
	committed        bool
	rolledBack       bool
}

func (f *fakeTx) Exec(ctx context.Context, sql string, args ...any) (pgxConnTag, error) {
	f.queries = append(f.queries, recordedQuery{kind: "exec", sql: sql})
	return fakeTag{rows: f.execRowsAffected}, nil
}

func (f *fakeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	f.queries = append(f.queries, recordedQuery{kind: "queryrow", sql: sql})
	return fakeRow{}
}

func (f *fakeTx) Commit(ctx context.Context) error   { f.committed = true; return nil }
func (f *fakeTx) Rollback(ctx context.Context) error { f.rolledBack = true; return nil }

type fakeTag struct{ rows int64 }

func (t fakeTag) RowsAffected() int64 { return t.rows }

type fakeRow struct{}

func (fakeRow) Scan(dest ...any) error {
	for _, d := range dest {
		switch p := d.(type) {
		case *time.Time:
			*p = time.Now()
		}
	}
	return nil
}

type pgxConnTag = interface{ RowsAffected() int64 }

func testStoreWithTx(tx pgx.Tx) *Store {
	return &Store{pool: &fakePool{tx: tx}}
}

// fakePool satisfies the pool face Store needs for withTenant.
type fakePool struct {
	tx pgx.Tx
}

func (p *fakePool) Begin(ctx context.Context) (pgx.Tx, error) { return p.tx, nil }

func containsStr(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
