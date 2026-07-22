package consumer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/opendesk/crm-sync-service/internal/events"
	"github.com/opendesk/crm-sync-service/internal/metrics"
	"github.com/opendesk/crm-sync-service/internal/syncmap"
	"github.com/opendesk/crm-sync-service/internal/twentyc"
	"go.uber.org/zap"
)

// privacyStub serves a Twenty API with exactly one person (id person-1) that
// can be found by email/phone and deleted.
type privacyStub struct {
	mu      sync.Mutex
	deleted []string
}

func (s *privacyStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		write := func(key string, v any) {
			b, _ := json.Marshal(map[string]any{"data": map[string]any{key: v}})
			_, _ = w.Write(b)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/people":
			filter := r.URL.Query().Get("filter")
			if strings.Contains(filter, "jane@example.com") || strings.Contains(filter, "%2B1555") || strings.Contains(filter, "+1555") {
				write("people", []map[string]any{{"id": "person-1"}})
				return
			}
			write("people", []map[string]any{})
		case r.Method == http.MethodDelete && r.URL.Path == "/rest/people/person-1":
			s.mu.Lock()
			s.deleted = append(s.deleted, "person-1")
			s.mu.Unlock()
			write("deleted", map[string]any{"id": "person-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func privacyEvent(phone, email string) events.CloudEvent {
	return events.CloudEvent{
		SpecVersion: "1.0",
		ID:          uuid.NewString(),
		Source:      "notification-worker",
		Type:        PrivacyEventType,
		Data: map[string]any{
			"phone":     phone,
			"email":     email,
			"tenant_id": uuid.NewString(),
		},
	}
}

func TestHandlePrivacy_DeletesPersonAndCleansSyncMap(t *testing.T) {
	stub := &privacyStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	fm := newFakeMap()
	s := &Syncer{
		Twenty:  twentyc.New(srv.URL, "k", 0),
		Map:     fm,
		Metrics: metrics.New(),
		Log:     zap.NewNop(),
	}
	tid := uuid.New()
	if err := fm.Put(context.Background(), syncmap.KindBookingContact, "b-1", "person-1", &tid); err != nil {
		t.Fatal(err)
	}

	if err := s.HandlePrivacy(context.Background(), privacyEvent("+15551234567", "jane@example.com")); err != nil {
		t.Fatalf("HandlePrivacy: %v", err)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.deleted) != 1 || stub.deleted[0] != "person-1" {
		t.Fatalf("expected person-1 deleted, got %v", stub.deleted)
	}
	if _, err := fm.Get(context.Background(), syncmap.KindBookingContact, "b-1", &tid); err != syncmap.ErrNotFound {
		t.Fatalf("expected sync_map cleanup, got err=%v", err)
	}
}

func TestHandlePrivacy_UnknownPersonIsAcknowledged(t *testing.T) {
	stub := &privacyStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	s := &Syncer{
		Twenty:  twentyc.New(srv.URL, "k", 0),
		Map:     newFakeMap(),
		Metrics: metrics.New(),
		Log:     zap.NewNop(),
	}
	if err := s.HandlePrivacy(context.Background(), privacyEvent("", "nobody@example.com")); err != nil {
		t.Fatalf("HandlePrivacy unknown person should succeed: %v", err)
	}
}

func TestHandlePrivacy_IgnoresOtherTypes(t *testing.T) {
	s := &Syncer{Metrics: metrics.New(), Log: zap.NewNop()}
	evt := privacyEvent("+1555", "")
	evt.Type = "SomethingElse"
	if err := s.HandlePrivacy(context.Background(), evt); err != nil {
		t.Fatalf("ignore: %v", err)
	}
}
