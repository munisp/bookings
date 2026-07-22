package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/opendesk/crm-sync-service/internal/metrics"
	"github.com/opendesk/crm-sync-service/internal/syncmap"
	"github.com/opendesk/crm-sync-service/internal/twentyc"
	"go.uber.org/zap"
)

// fakeMap is an in-memory MapReader.
type fakeMap struct {
	mu   sync.Mutex
	rows map[string]string
}

func (f *fakeMap) put(kind, odID string, tid *uuid.UUID, twentyID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := "nil"
	if tid != nil {
		t = tid.String()
	}
	f.rows[kind+"|"+odID+"|"+t] = twentyID
}

func (f *fakeMap) Get(_ context.Context, kind, odID string, tid *uuid.UUID) (syncmap.Mapping, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := "nil"
	if tid != nil {
		t = tid.String()
	}
	if id, ok := f.rows[kind+"|"+odID+"|"+t]; ok {
		return syncmap.Mapping{Kind: kind, OpenDeskID: odID, TwentyID: id}, nil
	}
	return syncmap.Mapping{}, syncmap.ErrNotFound
}

// twentyRecorder answers like Twenty and records calls.
type twentyRecorder struct {
	mu       sync.Mutex
	requests []string
	people   []map[string]any // returned for GET /rest/people
}

func (tr *twentyRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tr.mu.Lock()
		tr.requests = append(tr.requests, r.Method+" "+r.URL.Path)
		tr.mu.Unlock()
		write := func(key string, v any) {
			b, _ := json.Marshal(map[string]any{"data": map[string]any{key: v}})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/people":
			write("people", tr.people)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/tasks":
			write("createTask", map[string]any{"id": "task-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/taskTargets":
			write("created", map[string]any{"id": "tt-1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (tr *twentyRecorder) called(sub string) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for _, r := range tr.requests {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

func newTestServer(t *testing.T) (*Server, *fakeMap, *twentyRecorder) {
	t.Helper()
	rec := &twentyRecorder{}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)
	fm := &fakeMap{rows: map[string]string{}}
	return &Server{
		Twenty:  twentyc.New(srv.URL, "k", 0),
		Map:     fm,
		Metrics: metrics.New(),
		Log:     zap.NewNop(),
	}, fm, rec
}

func postTasks(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestCreateTaskStaffAlertUnlinked(t *testing.T) {
	s, _, rec := newTestServer(t)
	code, out := postTasks(t, s, `{
		"tenant_slug":"acme-clinic","tenant_id":"`+uuid.NewString()+`",
		"kind":"staff_alert","title":"Intake form incomplete — visit b-1",
		"body":"Patient has not completed intake.","booking_id":"b-1",
		"due_at":"2026-03-01T10:00:00Z"}`)
	if code != http.StatusCreated {
		t.Fatalf("code = %d, out = %v", code, out)
	}
	if out["linked"] != false {
		t.Fatalf("linked = %v, want false", out)
	}
	if out["taskId"] != "task-1" {
		t.Fatalf("taskId = %v", out)
	}
	if rec.called("/rest/taskTargets") {
		t.Fatal("unlinked task must not create a taskTarget")
	}
	if !rec.called("POST /rest/tasks") {
		t.Fatal("task not created")
	}
}

func TestCreateTaskFollowUpResolvesPersonViaBooking(t *testing.T) {
	s, fm, rec := newTestServer(t)
	tid := uuid.New()
	fm.put(syncmap.KindBookingContact, "b-9", &tid, "person-77")
	code, out := postTasks(t, s, `{
		"tenant_slug":"acme-consult","tenant_id":"`+tid.String()+`",
		"kind":"follow_up","title":"Follow up with Jane","body":"Send proposal.",
		"booking_id":"b-9","due_at":"2026-03-08T10:00:00Z"}`)
	if code != http.StatusCreated {
		t.Fatalf("code = %d, out = %v", code, out)
	}
	if out["personId"] != "person-77" || out["linked"] != true {
		t.Fatalf("out = %v, want linked to person-77", out)
	}
	if !rec.called("/rest/taskTargets") {
		t.Fatal("linked task should create a taskTarget")
	}
}

func TestCreateTaskFollowUpDegradesToUnlinked(t *testing.T) {
	s, _, rec := newTestServer(t)
	rec.people = []map[string]any{} // no person anywhere
	code, out := postTasks(t, s, `{
		"tenant_slug":"acme-consult","tenant_id":"`+uuid.NewString()+`",
		"kind":"follow_up","title":"Follow up","booking_id":"b-unknown",
		"phone":"+1555","due_at":"2026-03-08T10:00:00Z"}`)
	if code != http.StatusCreated {
		t.Fatalf("follow_up must degrade, not 4xx: code = %d, out = %v", code, out)
	}
	if out["linked"] != false {
		t.Fatalf("linked = %v", out)
	}
}

func TestCreateTaskCanonicalShapeStillWorks(t *testing.T) {
	s, _, rec := newTestServer(t)
	rec.people = []map[string]any{{"id": "person-1"}}
	code, out := postTasks(t, s, `{"email":"jane@example.com","title":"Call Jane","dueAt":"2026-03-02T09:00:00Z"}`)
	if code != http.StatusCreated || out["personId"] != "person-1" {
		t.Fatalf("code = %d out = %v", code, out)
	}
	if !rec.called("/rest/taskTargets") {
		t.Fatal("expected taskTarget link")
	}
}

func TestCreateTaskValidations(t *testing.T) {
	s, _, _ := newTestServer(t)
	if code, _ := postTasks(t, s, `{"body":"no title"}`); code != http.StatusBadRequest {
		t.Fatalf("missing title: code = %d", code)
	}
	if code, _ := postTasks(t, s, `{"title":"x","due_at":"not-a-date"}`); code != http.StatusBadRequest {
		t.Fatalf("bad due_at: code = %d", code)
	}
	if code, _ := postTasks(t, s, `{invalid`); code != http.StatusBadRequest {
		t.Fatalf("bad json: code = %d", code)
	}
}

func TestResolveDueAt(t *testing.T) {
	cases := []struct{ dueAt, snake, want string }{
		{"2026-03-01T10:00:00Z", "", "2026-03-01T10:00:00Z"},
		{"", "2026-03-01T10:00:00Z", "2026-03-01T10:00:00Z"},
		{"2026-03-01T11:00:00Z", "2026-03-01T10:00:00Z", "2026-03-01T11:00:00Z"}, // dueAt wins
		{"", "", ""},
	}
	for _, c := range cases {
		got, err := resolveDueAt(c.dueAt, c.snake)
		if err != nil {
			t.Fatalf("resolveDueAt(%q,%q): %v", c.dueAt, c.snake, err)
		}
		if got != c.want {
			t.Errorf("resolveDueAt(%q,%q) = %q, want %q", c.dueAt, c.snake, got, c.want)
		}
	}
	if _, err := resolveDueAt("", "tomorrow"); err == nil {
		t.Fatal("expected error for non-RFC3339 due_at")
	}
}
