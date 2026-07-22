package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/availability"
	"github.com/redis/go-redis/v9"
)

func newTestCache(t *testing.T) (*Cache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := newWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}), 120*time.Second, 15*time.Minute)
	t.Cleanup(func() { _ = c.Close() })
	return c, mr
}

func TestKeyFormat(t *testing.T) {
	tenant := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	offering := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	member := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	day := time.Date(2025, 3, 4, 23, 30, 0, 0, time.UTC)
	want := "avail:11111111-1111-1111-1111-111111111111:22222222-2222-2222-2222-222222222222:33333333-3333-3333-3333-333333333333:2025-03-04"
	if got := Key(tenant, offering, member, day); got != want {
		t.Fatalf("Key() = %q, want %q", got, want)
	}
}

func TestDayBuckets(t *testing.T) {
	from := time.Date(2025, 1, 30, 10, 0, 0, 0, time.UTC)
	to := time.Date(2025, 2, 2, 9, 0, 0, 0, time.UTC)
	days := DayBuckets(from, to)
	if len(days) != 4 {
		t.Fatalf("DayBuckets len = %d, want 4 (%v)", len(days), days)
	}
	for i, want := range []string{"2025-01-30", "2025-01-31", "2025-02-01", "2025-02-02"} {
		if got := days[i].Format("2006-01-02"); got != want {
			t.Fatalf("bucket %d = %s, want %s", i, got, want)
		}
	}
	if DayBuckets(to, from) != nil {
		t.Fatal("inverted range must yield nil")
	}
	if DayBuckets(from, from) != nil {
		t.Fatal("empty range must yield nil")
	}
}

func TestSetGetRoundTrip(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()
	key := Key(uuid.New(), uuid.New(), uuid.New(), time.Now())

	if _, hit := c.GetSlots(ctx, key); hit {
		t.Fatal("expected miss on empty cache")
	}

	slots := []availability.Slot{
		{StartsAt: time.Date(2025, 5, 1, 9, 0, 0, 0, time.UTC), EndsAt: time.Date(2025, 5, 1, 9, 30, 0, 0, time.UTC)},
		{StartsAt: time.Date(2025, 5, 1, 10, 0, 0, 0, time.UTC), EndsAt: time.Date(2025, 5, 1, 10, 30, 0, 0, time.UTC)},
	}
	c.SetSlots(ctx, key, slots)

	got, hit := c.GetSlots(ctx, key)
	if !hit {
		t.Fatal("expected hit after SetSlots")
	}
	if len(got) != 2 || !got[0].StartsAt.Equal(slots[0].StartsAt) || !got[1].EndsAt.Equal(slots[1].EndsAt) {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestSetSlotsHonoursTTL(t *testing.T) {
	c, mr := newTestCache(t)
	ctx := context.Background()
	key := Key(uuid.New(), uuid.New(), uuid.New(), time.Now())
	c.SetSlots(ctx, key, nil)

	if ttl := mr.TTL(key); ttl != 120*time.Second {
		t.Fatalf("TTL = %v, want 120s", ttl)
	}
	mr.FastForward(121 * time.Second)
	if _, hit := c.GetSlots(ctx, key); hit {
		t.Fatal("entry should have expired after TTL")
	}
}

func TestInvalidateDeletesOnlyCoveredDays(t *testing.T) {
	c, mr := newTestCache(t)
	ctx := context.Background()
	tenant, offering, member := uuid.New(), uuid.New(), uuid.New()

	d1 := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	d2 := d1.AddDate(0, 0, 1)
	d3 := d1.AddDate(0, 0, 2)
	for _, d := range []time.Time{d1, d2, d3} {
		c.SetSlots(ctx, Key(tenant, offering, member, d), nil)
	}
	// Another member's key on an invalidated day must survive.
	other := Key(tenant, offering, uuid.New(), d2)
	c.SetSlots(ctx, other, nil)

	// Booking spanning d1 22:00 → d2 01:00 invalidates d1 and d2, not d3.
	c.Invalidate(ctx, tenant, offering, member, d1.Add(22*time.Hour), d2.Add(time.Hour))

	if mr.Exists(Key(tenant, offering, member, d1)) {
		t.Fatal("d1 should be invalidated")
	}
	if mr.Exists(Key(tenant, offering, member, d2)) {
		t.Fatal("d2 should be invalidated")
	}
	if !mr.Exists(Key(tenant, offering, member, d3)) {
		t.Fatal("d3 should survive")
	}
	if !mr.Exists(other) {
		t.Fatal("other member's key should survive")
	}
}

func TestNilCacheIsNoOp(t *testing.T) {
	var c *Cache
	ctx := context.Background()
	if c.Enabled() {
		t.Fatal("nil cache must report disabled")
	}
	if _, hit := c.GetSlots(ctx, "k"); hit {
		t.Fatal("nil cache must miss")
	}
	c.SetSlots(ctx, "k", nil)                                                                    // must not panic
	c.Invalidate(ctx, uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Now().Add(time.Hour)) // must not panic
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

// --- Serve-stale (Wave 5 #5) -------------------------------------------------

func staleTestSlots() []availability.Slot {
	return []availability.Slot{
		{StartsAt: time.Date(2025, 5, 1, 9, 0, 0, 0, time.UTC), EndsAt: time.Date(2025, 5, 1, 9, 30, 0, 0, time.UTC)},
	}
}

func TestStaleCopyServedAfterFreshExpiry(t *testing.T) {
	c, mr := newTestCache(t)
	ctx := context.Background()
	key := Key(uuid.New(), uuid.New(), uuid.New(), time.Now())
	c.SetSlots(ctx, key, staleTestSlots())

	// Fresh entry expires after 120s; the stale copy (TTL 120s+15m) remains.
	mr.FastForward(121 * time.Second)
	if _, hit := c.GetSlots(ctx, key); hit {
		t.Fatal("fresh entry should have expired")
	}
	got, hit := c.GetSlotsStale(ctx, key)
	if !hit {
		t.Fatal("stale copy should be served within the stale window")
	}
	if len(got) != 1 || !got[0].StartsAt.Equal(staleTestSlots()[0].StartsAt) {
		t.Fatalf("stale payload mismatch: %+v", got)
	}

	// Past the stale window (fresh 120s + stale 15m) the Redis stale copy is
	// gone. (The in-process copy expires on wall clock — covered by
	// TestMemStaleCopyExpires.)
	mr.FastForward(15*time.Minute + time.Second)
	if mr.Exists(staleKey(key)) {
		t.Fatal("redis stale copy must expire after fresh TTL + stale TTL")
	}
}

func TestMemStaleCopyExpires(t *testing.T) {
	mr := miniredis.RunT(t)
	c := newWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}), 20*time.Millisecond, 20*time.Millisecond)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	key := Key(uuid.New(), uuid.New(), uuid.New(), time.Now())
	c.SetSlots(ctx, key, staleTestSlots())

	mr.FastForward(41 * time.Millisecond) // expire the Redis copies (miniredis clock)
	time.Sleep(60 * time.Millisecond)     // beyond fresh TTL + stale TTL (wall clock, in-process copy)
	if _, hit := c.GetSlotsStale(ctx, key); hit {
		t.Fatal("in-process stale copy must expire after fresh TTL + stale TTL")
	}
}

func TestStaleCopyServedFromMemoryOnRedisOutage(t *testing.T) {
	c, mr := newTestCache(t)
	ctx := context.Background()
	key := Key(uuid.New(), uuid.New(), uuid.New(), time.Now())
	c.SetSlots(ctx, key, staleTestSlots())

	// Redis disappears entirely — the in-process copy still answers.
	mr.Close()
	if _, hit := c.GetSlots(ctx, key); hit {
		t.Fatal("fresh read during outage must miss")
	}
	got, hit := c.GetSlotsStale(ctx, key)
	if !hit {
		t.Fatal("in-process stale copy should survive a Redis outage")
	}
	if len(got) != 1 {
		t.Fatalf("stale payload mismatch: %+v", got)
	}
}

func TestStaleMissWithoutEntry(t *testing.T) {
	c, _ := newTestCache(t)
	if _, hit := c.GetSlotsStale(context.Background(), Key(uuid.New(), uuid.New(), uuid.New(), time.Now())); hit {
		t.Fatal("stale read on empty cache must miss")
	}
	var nilCache *Cache
	if _, hit := nilCache.GetSlotsStale(context.Background(), "k"); hit {
		t.Fatal("nil cache must miss")
	}
}

func TestInvalidateClearsStaleCopies(t *testing.T) {
	c, mr := newTestCache(t)
	ctx := context.Background()
	tenant, offering, member := uuid.New(), uuid.New(), uuid.New()
	day := time.Date(2025, 6, 10, 0, 0, 0, 0, time.UTC)
	key := Key(tenant, offering, member, day)
	c.SetSlots(ctx, key, staleTestSlots())

	c.Invalidate(ctx, tenant, offering, member, day.Add(time.Hour), day.Add(2*time.Hour))

	if _, hit := c.GetSlots(ctx, key); hit {
		t.Fatal("fresh entry should be invalidated")
	}
	if _, hit := c.GetSlotsStale(ctx, key); hit {
		t.Fatal("stale entry should be invalidated")
	}
	if mr.Exists(staleKey(key)) {
		t.Fatal("redis stale key should be deleted")
	}
}
