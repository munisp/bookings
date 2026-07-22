package pacer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeRedis implements RedisClient with scripted Eval results and failures.
type fakeRedis struct {
	evalResults []int64 // wait-ms values returned in order, last one repeats
	evalCalls   int
	evalErr     error
	incrVal     int64
	incrErr     error
}

func (f *fakeRedis) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	if f.evalErr != nil {
		cmd.SetErr(f.evalErr)
		return cmd
	}
	var v int64
	if f.evalCalls < len(f.evalResults) {
		v = f.evalResults[f.evalCalls]
	} else if len(f.evalResults) > 0 {
		v = f.evalResults[len(f.evalResults)-1]
	}
	f.evalCalls++
	cmd.SetVal(v)
	return cmd
}

func (f *fakeRedis) Incr(ctx context.Context, key string) *redis.IntCmd {
	cmd := redis.NewIntCmd(ctx)
	if f.incrErr != nil {
		cmd.SetErr(f.incrErr)
		return cmd
	}
	f.incrVal++
	cmd.SetVal(f.incrVal)
	return cmd
}

func testConfig() Config {
	return Config{CPS: 1.0, Burst: 3, Backend: "local", RedisAddr: "redis:6379"}
}

// Burst respected: OUTBOUND_BURST sends start immediately, the next one
// waits for a token refill at OUTBOUND_CPS.
func TestLocalBurstRespected(t *testing.T) {
	p := New(testConfig(), zap.NewNop())
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 3; i++ {
		require.NoError(t, p.Wait(ctx))
	}
	require.Less(t, time.Since(start), 300*time.Millisecond, "burst of 3 must be immediate")

	require.NoError(t, p.Wait(ctx))
	require.GreaterOrEqual(t, time.Since(start), 800*time.Millisecond,
		"4th send at 1 CPS must wait ~1s for a token")
}

// Rotation cycles round-robin through OUTBOUND_FROM_NUMBERS (local counter).
func TestLocalRotationCycles(t *testing.T) {
	cfg := testConfig()
	cfg.FromNumbers = []string{"+15550000001", "+15550000002"}
	p := New(cfg, zap.NewNop())
	ctx := context.Background()

	got := make([]string, 5)
	for i := range got {
		got[i] = p.NextSender(ctx)
	}
	require.Equal(t, []string{"+15550000001", "+15550000002", "+15550000001", "+15550000002", "+15550000001"}, got)
}

// No rotation pool configured → empty sender, caller keeps its default.
func TestRotationEmptyPool(t *testing.T) {
	p := New(testConfig(), zap.NewNop())
	require.Equal(t, "", p.NextSender(context.Background()))
}

// Redis-down FAIL-OPEN: the pacer logs a warning once and falls back to the
// local limiter / local rotation instead of dropping the send.
func TestRedisDownFallsBackToLocal(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	cfg := testConfig()
	cfg.Backend = "redis"
	cfg.FromNumbers = []string{"+15550000001", "+15550000002"}
	p := NewWithClient(cfg, &fakeRedis{evalErr: errors.New("connection refused"), incrErr: errors.New("connection refused")}, zap.New(core))
	ctx := context.Background()

	start := time.Now()
	require.NoError(t, p.Wait(ctx))
	require.NoError(t, p.Wait(ctx))
	require.Less(t, time.Since(start), 300*time.Millisecond, "fallback to local burst must be immediate")

	// Rotation also falls back to the local atomic counter.
	require.Equal(t, "+15550000001", p.NextSender(ctx))
	require.Equal(t, "+15550000002", p.NextSender(ctx))

	warns := logs.FilterMessageSnippet("failing open to local rate limiter").All()
	require.Len(t, warns, 1, "redis-down warning is logged once, not per send")
}

// Redis backend: a granted token (wait 0) returns immediately; a positive
// retry delay is slept before re-acquiring.
func TestRedisTokenBucketWait(t *testing.T) {
	cfg := testConfig()
	cfg.Backend = "redis"
	fake := &fakeRedis{evalResults: []int64{200, 0}}
	p := NewWithClient(cfg, fake, zap.NewNop())

	start := time.Now()
	require.NoError(t, p.Wait(context.Background()))
	require.GreaterOrEqual(t, time.Since(start), 180*time.Millisecond, "must sleep the bucket's retry delay")
	require.Equal(t, 2, fake.evalCalls, "retry after the delay")
}

// Redis rotation uses the shared INCR counter (fleet-wide round-robin).
func TestRedisRotationUsesIncr(t *testing.T) {
	cfg := testConfig()
	cfg.Backend = "redis"
	cfg.FromNumbers = []string{"+15550000001", "+15550000002"}
	p := NewWithClient(cfg, &fakeRedis{}, zap.NewNop())
	ctx := context.Background()

	got := make([]string, 3)
	for i := range got {
		got[i] = p.NextSender(ctx)
	}
	require.Equal(t, []string{"+15550000001", "+15550000002", "+15550000001"}, got)
}
