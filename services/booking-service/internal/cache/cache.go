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
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/availability"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// DefaultTTL is used when CACHE_TTL_SECONDS is unset (SPEC-W3 §3: 120s).
const DefaultTTL = 120 * time.Second

// Cache wraps a go-redis client. A nil *Cache is a valid, disabled cache:
// all methods are no-ops / misses, so callers need no nil checks.
type Cache struct {
	rdb redis.UniversalClient
	ttl time.Duration
	log *zap.Logger
}

// New connects to Redis at addr (REDIS_ADDR). Empty addr disables caching
// (returns nil). ttl <= 0 falls back to DefaultTTL.
func New(addr string, ttl time.Duration, log *zap.Logger) *Cache {
	if addr == "" {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if log == nil {
		log = zap.NewNop()
	}
	return &Cache{
		rdb: redis.NewClient(&redis.Options{Addr: addr}),
		ttl: ttl,
		log: log,
	}
}

// newWithClient builds a Cache around an existing client (tests).
func newWithClient(rdb redis.UniversalClient, ttl time.Duration) *Cache {
	return &Cache{rdb: rdb, ttl: ttl, log: zap.NewNop()}
}

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

// SetSlots stores a slot list under key with the configured TTL. Failures
// are logged and swallowed — caching is an optimization, never fatal.
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
	keys := make([]string, 0, len(buckets))
	for _, d := range buckets {
		keys = append(keys, Key(tenantID, offeringID, memberID, d))
	}
	if err := c.rdb.Del(ctx, keys...).Err(); err != nil {
		c.log.Warn("availability cache invalidation failed", zap.Strings("keys", keys), zap.Error(err))
	}
}

// Close releases the Redis client.
func (c *Cache) Close() error {
	if c == nil {
		return nil
	}
	return c.rdb.Close()
}
