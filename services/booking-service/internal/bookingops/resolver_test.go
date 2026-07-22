package bookingops

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/daprc"
	"go.uber.org/zap"
)

// fakeDaprd fakes the identity-service invocation endpoint behind Dapr.
type fakeDaprd struct {
	srv    *httptest.Server
	calls  atomic.Int32
	broken atomic.Bool
	tenant TenantInfo
}

func newFakeDaprd(t *testing.T) *fakeDaprd {
	t.Helper()
	f := &fakeDaprd{tenant: TenantInfo{
		ID:       uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Name:     "Acme",
		Timezone: "Europe/Berlin",
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.broken.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		f.calls.Add(1)
		_ = json.NewEncoder(w).Encode(f.tenant)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDaprd) client(t *testing.T) *daprc.Client {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return daprc.New(host, port)
}

func TestResolverCachesWithinTTL(t *testing.T) {
	f := newFakeDaprd(t)
	r := NewTenantResolver(f.client(t), "identity", time.Minute, zap.NewNop())
	ctx := context.Background()

	t1, err := r.BySlug(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if t1.ID != f.tenant.ID || t1.Slug != "acme" || t1.Timezone != "Europe/Berlin" {
		t.Fatalf("unexpected tenant: %+v", t1)
	}
	if _, err := r.BySlug(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("identity-service called %d times, want 1 (TTL cache)", f.calls.Load())
	}
}

func TestResolverRefreshesAfterTTL(t *testing.T) {
	f := newFakeDaprd(t)
	r := NewTenantResolver(f.client(t), "identity", 30*time.Millisecond, zap.NewNop())
	ctx := context.Background()

	if _, err := r.BySlug(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := r.BySlug(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("identity-service called %d times, want 2 (refresh after TTL)", f.calls.Load())
	}
}

// identity-service outage after the TTL expired: the stale cached entry is
// served instead of failing (Wave 5 #5).
func TestResolverServesStaleOnIdentityOutage(t *testing.T) {
	f := newFakeDaprd(t)
	r := NewTenantResolver(f.client(t), "identity", 30*time.Millisecond, zap.NewNop())
	ctx := context.Background()

	if _, err := r.BySlug(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond) // let the entry expire
	f.broken.Store(true)

	got, err := r.BySlug(ctx, "acme")
	if err != nil {
		t.Fatalf("stale entry should be served without error, got %v", err)
	}
	if got.ID != f.tenant.ID || got.Timezone != "Europe/Berlin" {
		t.Fatalf("stale tenant mismatch: %+v", got)
	}
}

// With no prior successful resolution, an outage is a hard error.
func TestResolverErrorsWhenNeverResolved(t *testing.T) {
	f := newFakeDaprd(t)
	f.broken.Store(true)
	r := NewTenantResolver(f.client(t), "identity", time.Minute, zap.NewNop())
	if _, err := r.BySlug(context.Background(), "acme"); err == nil {
		t.Fatal("expected error when identity-service is down and cache is cold")
	}
}
