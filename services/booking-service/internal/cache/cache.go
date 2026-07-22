// Package cache implements the Redis-backed availability cache
// (SPEC-W3 §3). Computed slot lists are cached per
// tenant/offering/team-member/UTC-day under
//
//	avail:{tenant}:{offering}:{member}:{YYYY-MM-DD}
//
// with a short TTL (default 120s, CACHE_TTL_SECONDS). Write paths
// (booking create/reschedule/cancel — REST, public and Kafka consumer all
// funnel through bookingops) invalidate the day-range keys of the affected
// member so stale openings survive at most one TTL window.
//
// Day buckets are UTC calendar days: bucketing must be identical between
// the read path (which knows the tenant timezone) and the invalidation
// path (bookingops.Cancel does not), and UTC is the only timezone both
// share. Bucket boundaries only affect cache granularity, not correctness.
//
// Serve-stale (Wave 5 #5): every SetSlots also writes a stale copy under
// "stale:"+key with TTL fresh+staleTTL, plus an in-process copy that
// survives a Redis outage. When the read path cannot compute a fresh answer
// (Postgres error) or Redis itself is down, the handler falls back to
// GetSlotsStale and answers with X-Cache: stale for at most staleTTL past
// the fresh TTL. Stale copies are invalidated together with fresh ones, so
// a booking write never leaves an already-known-wrong value servable.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/availability"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// DefaultTTL is used when CACHE_TTL_SECONDS is unset (SPEC-W3 §3: 120s).
const DefaultTTL = 120 * time.Second

// DefaultStaleTTL is used when CACHE_STALE_TTL_SECONDS is unset (15min).
const DefaultStaleTTL = 15 * time.Minute

// memEntry is the in-process last-known-good copy (survives Redis outages).
type memEntry struct {
	slots     []availability.Slot
	expiresAt time.Time
}

// Cache wraps a go-redis client. A nil *Cache is a valid, disabled cache:
// all methods are no-ops / misses, so callers need no nil checks.
type Cache struct {
	rdb      redis.UniversalClient
	ttl      time.Duration
	staleTTL time.Duration
	log      *zap.Logger

	mu       sync.Mutex
	memStale map[string]memEntry
}

// New connects to Redis at addr (REDIS_ADDR). Empty addr disables caching
// (returns nil). ttl <= 0 falls back to DefaultTTL, staleTTL <= 0 to
// DefaultStaleTTL.
func New(addr string, ttl, staleTTL time.Duration, log *zap.Logger) *Cache {
	if addr == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if staleTTL <= 0 {
		staleTTL = DefaultStaleTTL
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Cache{
		rdb:      redis.NewClient(&redis.Options{Addr: addr}),
		ttl:      ttl,
		staleTTL: staleTTL,
		log:      log,
		memStale: map[string]memEntry{},
	}
}

// newWithClient builds a Cache around an existing client (tests).
func newWithClient(rdb redis.UniversalClient, ttl, staleTTL time.Duration) *Cache {
	if staleTTL <= 0 {
		staleTTL = DefaultStaleTTL
	}
	return &Cache{rdb: rdb, ttl: ttl, staleTTL: staleTTL, log: zap.NewNop(), memStale: map[string]memEntry{}}
}

// staleKey returns the key of the last-known-good copy for a fresh key.
func staleKey(key string) string { return "stale:" + key }

// Key returns the cache key for one UTC day bucket.
func Key(tenantID, offeringID, memberID uuid.UUID, day time.Time) string {
	return "avail:" + tenantID.String() + ":" + offeringID.String() + ":" +
		memberID.String() + ":" + day.UTC().Format("2006-01-02")
}

// DayBuckets returns the UTC calendar days covering [from, to). An invalid
// or empty range yields nil.
func DayBuckets(from, to time.Time) []time.Time {
	if !to.After(from) {
		return nil
	}
	day := time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC)
	var out []time.Time
	for day.Before(to.UTC()) {
		out = append(out, day)
		day = day.AddDate(0, 0, 1)
	}
	return out
}

// Enabled reports whether caching is active.
func (c *Cache) Enabled() bool { return c != nil }

// GetSlots returns the cached slot list for a key. The second return value
// is false on a miss — including Redis errors, which are logged and treated
// as misses so a Redis outage degrades to uncached computation.
func (c *Cache) GetSlots(ctx context.Context, key string) ([]availability.Slot, bool) {
	if c == nil {
		return nil, false
	}
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			c.log.Warn("availability cache read failed; treating as miss", zap.Error(err))
		}
		return nil, false
	}
	var slots []availability.Slot
	if err := json.Unmarshal(raw, &slots); err != nil {
		c.log.Warn("availability cache entry corrupt; treating as miss", zap.Error(err))
		return nil, false
	}
	return slots, true
}

// SetSlots stores a slot list under key with the configured TTL, refreshes
// the stale copies (Redis "stale:"+key with TTL fresh+staleTTL, and the
// in-process fallback). Failures are logged and swallowed — caching is an
// optimization, never fatal.
func (c *Cache) SetSlots(ctx context.Context, key string, slots []availability.Slot) {
	if c == nil {
		return
	}
	if slots == nil {
		slots = []availability.Slot{}
	}
	raw, err := json.Marshal(slots)
	if err != nil {
		c.log.Warn("availability cache marshal failed", zap.Error(err))
		return
	}
	if err := c.rdb.Set(ctx, key, raw, c.ttl).Err(); err != nil {
		c.log.Warn("availability cache write failed", zap.Error(err))
	}
	if err := c.rdb.Set(ctx, staleKey(key), raw, c.ttl+c.staleTTL).Err(); err != nil {
		c.log.Warn("availability stale-cache write failed", zap.Error(err))
	}
	c.mu.Lock()
	c.memStale[key] = memEntry{slots: slots, expiresAt: time.Now().Add(c.ttl + c.staleTTL)}
	c.mu.Unlock()
}

// GetSlotsStale returns the last-known-good slot list for key: first the
// Redis stale copy, then the in-process copy (which survives a Redis
// outage). The second return value is false when nothing usable is cached
// or the stale window (fresh TTL + staleTTL) has passed.
func (c *Cache) GetSlotsStale(ctx context.Context, key string) ([]availability.Slot, bool) {
	if c == nil {
		return nil, false
	}
	raw, err := c.rdb.Get(ctx, staleKey(key)).Bytes()
	switch {
	case err == nil:
		var slots []availability.Slot
		if uerr := json.Unmarshal(raw, &slots); uerr != nil {
			c.log.Warn("availability stale entry corrupt", zap.Error(uerr))
			return nil, false
		}
		return slots, true
	case errors.Is(err, redis.Nil):
		// no Redis stale copy — fall through to the in-process copy
	default:
		c.log.Warn("availability stale read failed; trying in-process copy", zap.Error(err))
	}
	return c.memGet(key)
}

// memGet returns the unexpired in-process stale copy.
func (c *Cache) memGet(key string) ([]availability.Slot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.memStale[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.slots, true
}

// Invalidate deletes the day-bucket keys covering [from, to] for one
// tenant/offering/member. Called by booking write paths after create,
// reschedule (old AND new range) and cancel. Failures are logged, not
// fatal: entries expire on their own within one TTL.
func (c *Cache) Invalidate(ctx context.Context, tenantID, offeringID, memberID uuid.UUID, from, to time.Time) {
	if c == nil {
		return
	}
	// Inclusive of both endpoints: a booking ending exactly at midnight
	// still occupies its start day; pad `to` by a microsecond so the final
	// touched day is always covered.
	buckets := DayBuckets(from, to.Add(time.Microsecond))
	if len(buckets) == 0 {
		return
	}
	keys := make([]string, 0, 2*len(buckets))
	for _, d := range buckets {
		k := Key(tenantID, offeringID, memberID, d)
		keys = append(keys, k, staleKey(k))
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		c.log.Warn("availability cache invalidation failed", zap.Strings("keys", keys), zap.Error(err))
	}
	c.mu.Lock()
	for _, k := range keys {
		delete(c.memStale, k)
	}
	c.mu.Unlock()
}

// Close releases the Redis client.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.rdb.Close()
}
