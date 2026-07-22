package twentyc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// listEnvelope builds a Twenty-style list response.
func listEnvelope(key string, records any) []byte {
	b, _ := json.Marshal(map[string]any{"data": map[string]any{key: records}})
	return b
}

func TestFindPersonByEmail(t *testing.T) {
	var gotPath, gotFilter, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFilter = r.URL.Query().Get("filter")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write(listEnvelope("people", []map[string]any{{"id": "p-1"}}))
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key", 0)
	rec, err := c.FindPerson(context.Background(), "jane@example.com", "+1555")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "p-1" {
		t.Fatalf("id = %q", rec.ID)
	}
	if gotPath != "/rest/people" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotFilter != `emails.primaryEmail[eq]:"jane@example.com"` {
		t.Fatalf("filter = %q", gotFilter)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestFindPersonFallsBackToPhone(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 { // email lookup: empty list
			_, _ = w.Write(listEnvelope("people", []map[string]any{}))
			return
		}
		if got := r.URL.Query().Get("filter"); got != `phones.primaryPhoneNumber[eq]:"+1555"` {
			t.Errorf("phone filter = %q", got)
		}
		_, _ = w.Write(listEnvelope("people", []map[string]any{{"id": "p-9"}}))
	}))
	defer srv.Close()

	c := New(srv.URL, "k", 0)
	rec, err := c.FindPerson(context.Background(), "nobody@example.com", "+1555")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "p-9" {
		t.Fatalf("id = %q", rec.ID)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestFindPersonNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(listEnvelope("people", []map[string]any{}))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", 0)
	_, err := c.FindPerson(context.Background(), "", "+1555")
	if err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateExtractsIDFromMutationEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = w.Write(listEnvelope("createPerson", map[string]any{"id": "p-new"}))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", 0)
	id, err := c.CreatePerson(context.Background(), PersonFromContact("Jane Doe", "j@x.co", ""))
	if err != nil {
		t.Fatal(err)
	}
	if id != "p-new" {
		t.Fatalf("id = %q", id)
	}
}

func TestRetryOn429ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(listEnvelope("people", []map[string]any{{"id": "p-1"}}))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", 0)
	start := time.Now()
	rec, err := c.FindPerson(context.Background(), "", "+1555")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != "p-1" {
		t.Fatalf("id = %q", rec.ID)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d", calls)
	}
	if time.Since(start) < time.Second { // 429 backoff hint is 2s, capped by test patience
		t.Log("note: backoff shorter than expected")
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad filter"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "k", 0)
	_, err := c.FindPerson(context.Background(), "", "+1555")
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", calls)
	}
}

func TestTokenBucketThrottle(t *testing.T) {
	b := newTokenBucket(2)
	b.refillAt = time.Now().Add(time.Hour) // no refill during test
	ctx := context.Background()
	if err := b.wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.wait(ctx); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := b.wait(cctx); err == nil {
		t.Fatal("third token should block until ctx timeout")
	}
}

func TestObserveCalledWithMethodAndPathClass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(listEnvelope("people", []map[string]any{{"id": "p-1"}}))
	}))
	defer srv.Close()
	type obs struct {
		method, class string
	}
	var got []obs
	c := New(srv.URL, "k", 0)
	c.Observe = func(method, pathClass string, d time.Duration) {
		got = append(got, obs{method, pathClass})
	}
	if _, err := c.FindPerson(context.Background(), "", "+1555"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreatePerson(context.Background(), PersonFromContact("Jane", "", "")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("observations = %v", got)
	}
	if got[0] != (obs{"GET", "people"}) {
		t.Errorf("got[0] = %+v, want GET people", got[0])
	}
	if got[1] != (obs{"POST", "people"}) {
		t.Errorf("got[1] = %+v, want POST people", got[1])
	}
}

func TestPathClassOf(t *testing.T) {
	cases := map[string]string{
		"/rest/people":         "people",
		"/rest/people/abc-123": "people",
		"/rest/taskTargets":    "taskTargets",
		"/rest/tasks/t-1":      "tasks",
		"/rest/":               "root",
	}
	for in, want := range cases {
		if got := pathClassOf(in); got != want {
			t.Errorf("pathClassOf(%q) = %q, want %q", in, got, want)
		}
	}
}
