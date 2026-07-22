package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/bookingops"
	"go.uber.org/zap"
)

// failingAuthz simulates a Permify outage.
type failingAuthz struct{ err error }

func (f failingAuthz) Check(ctx context.Context, tenantID, subject, permission, resource string) (bool, error) {
	return false, f.err
}

// allowAuthz always permits (control case).
type allowAuthz struct{}

func (allowAuthz) Check(ctx context.Context, tenantID, subject, permission, resource string) (bool, error) {
	return true, nil
}

func requestWithTenantUser() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/bookings", nil)
	ctx := context.WithValue(r.Context(), ctxTenant, bookingops.TenantInfo{ID: uuid.New(), Slug: "acme"})
	ctx = context.WithValue(ctx, ctxUser, "user-1")
	return r.WithContext(ctx)
}

// AUTHZ_OUTAGE_POLICY=fail_closed (default): a Permify error denies the
// request with 502 — current production behavior.
func TestAuthzOutageFailClosed(t *testing.T) {
	s := &server{d: Deps{
		Authz:             failingAuthz{err: errors.New("permify: connection refused")},
		AuthzOutagePolicy: AuthzFailClosed,
		Logger:            zap.NewNop(),
	}}
	reached := false
	h := s.require("manage_bookings")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithTenantUser())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if reached {
		t.Fatal("handler must not run when Permify fails closed")
	}
}

// Empty policy defaults to fail-closed.
func TestAuthzOutageDefaultIsFailClosed(t *testing.T) {
	s := &server{d: Deps{
		Authz:  failingAuthz{err: errors.New("permify down")},
		Logger: zap.NewNop(),
	}}
	reached := false
	h := s.require("manage_bookings")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithTenantUser())
	if rec.Code != http.StatusBadGateway || reached {
		t.Fatalf("default policy must fail closed (status %d, reached %v)", rec.Code, reached)
	}
}

// AUTHZ_OUTAGE_POLICY=fail_open: a Permify error logs CRITICAL and lets the
// request through (dev only).
func TestAuthzOutageFailOpen(t *testing.T) {
	s := &server{d: Deps{
		Authz:             failingAuthz{err: errors.New("permify down")},
		AuthzOutagePolicy: AuthzFailOpen,
		Logger:            zap.NewNop(),
	}}
	reached := false
	h := s.require("manage_bookings")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusCreated)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestWithTenantUser())
	if !reached || rec.Code != http.StatusCreated {
		t.Fatalf("fail_open must allow the request (status %d, reached %v)", rec.Code, reached)
	}
}

// A successful Permify check behaves identically under both policies.
func TestAuthzHealthyUnaffected(t *testing.T) {
	for _, policy := range []string{AuthzFailClosed, AuthzFailOpen} {
		s := &server{d: Deps{Authz: allowAuthz{}, AuthzOutagePolicy: policy, Logger: zap.NewNop()}}
		reached := false
		h := s.require("manage_bookings")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, requestWithTenantUser())
		if !reached {
			t.Fatalf("policy %s: healthy Permify must allow", policy)
		}
	}
}
