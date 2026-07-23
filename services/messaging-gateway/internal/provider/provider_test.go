package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/opendesk/messaging-gateway/internal/metrics"
	"go.uber.org/zap"
)

func testClient(name string) *Client {
	c := NewClient(name, metrics.New(), zap.NewNop())
	c.sleep = func(context.Context, int) {} // no backoff in tests
	return c
}

// fakeProvider counts requests and replies with the scripted statuses.
type fakeProvider struct {
	t        *testing.T
	calls    atomic.Int32
	statuses []int
	// captured request details of the first call
	contentType string
	authHeader  string
	apiKeyHdr   string
	body        []byte
	path        string
}

func (f *fakeProvider) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		if n == 1 {
			f.contentType = r.Header.Get("Content-Type")
			f.authHeader = r.Header.Get("Authorization")
			f.apiKeyHdr = r.Header.Get("apiKey")
			f.path = r.URL.Path
			f.body, _ = io.ReadAll(r.Body)
		}
		idx := int(n) - 1
		if idx >= len(f.statuses) {
			idx = len(f.statuses) - 1
		}
		status := f.statuses[idx]
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 400 {
			w.Write([]byte(`{"error":"provider says no"}`)) //nolint:errcheck
			return
		}
		w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
	}))
}

// runCase exercises the success / 4xx-no-retry / 5xx-retry matrix against a
// send function.
func runCase(t *testing.T, send func(baseURL string, f *fakeProvider) error) {
	t.Helper()

	t.Run("success", func(t *testing.T) {
		f := &fakeProvider{t: t, statuses: []int{200}}
		srv := f.server()
		defer srv.Close()
		if err := send(srv.URL, f); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := f.calls.Load(); got != 1 {
			t.Fatalf("expected 1 provider call, got %d", got)
		}
	})

	t.Run("4xx_no_retry", func(t *testing.T) {
		f := &fakeProvider{t: t, statuses: []int{400}}
		srv := f.server()
		defer srv.Close()
		err := send(srv.URL, f)
		if err == nil {
			t.Fatal("expected error on 400")
		}
		if !ClientError(err) {
			t.Fatalf("expected client error classification, got %v", err)
		}
		if got := f.calls.Load(); got != 1 {
			t.Fatalf("4xx must not be retried, got %d calls", got)
		}
	})

	t.Run("5xx_retry", func(t *testing.T) {
		f := &fakeProvider{t: t, statuses: []int{500, 503, 200}}
		srv := f.server()
		defer srv.Close()
		if err := send(srv.URL, f); err != nil {
			t.Fatalf("expected success after retries, got %v", err)
		}
		if got := f.calls.Load(); got != 3 {
			t.Fatalf("expected 3 calls (2 retries), got %d", got)
		}
	})

	t.Run("429_retry_then_fail", func(t *testing.T) {
		f := &fakeProvider{t: t, statuses: []int{429, 429, 429}}
		srv := f.server()
		defer srv.Close()
		err := send(srv.URL, f)
		if err == nil {
			t.Fatal("expected error after exhausting retries")
		}
		if ClientError(err) {
			t.Fatalf("429 must not be classified as client error: %v", err)
		}
		if got := f.calls.Load(); got != 3 {
			t.Fatalf("expected 3 calls, got %d", got)
		}
	})
}

func TestTermii(t *testing.T) {
	var last *fakeProvider
	runCase(t, func(baseURL string, f *fakeProvider) error {
		last = f
		p := &Termii{Client: testClient("termii"), BaseURL: baseURL, APIKey: "key-123", SenderID: "OpenDesk"}
		_, _, err := p.SendSMS(context.Background(), "+2348012345678", "hello", "")
		return err
	})

	// Request shape assertions (captured from the success run).
	f := last
	if f.path != "/api/sms/send" {
		t.Fatalf("unexpected path %q", f.path)
	}
	var payload map[string]string
	if err := json.Unmarshal(f.body, &payload); err != nil {
		t.Fatalf("decode termii payload: %v", err)
	}
	want := map[string]string{
		"api_key": "key-123", "to": "+2348012345678", "from": "OpenDesk",
		"sms": "hello", "type": "plain", "channel": "generic",
	}
	for k, v := range want {
		if payload[k] != v {
			t.Fatalf("payload[%s] = %q, want %q (full: %s)", k, payload[k], v, f.body)
		}
	}
}

func TestTermiiSenderIDOverride(t *testing.T) {
	f := &fakeProvider{t: t, statuses: []int{200}}
	srv := f.server()
	defer srv.Close()
	p := &Termii{Client: testClient("termii"), BaseURL: srv.URL, APIKey: "k", SenderID: "OpenDesk"}
	if _, _, err := p.SendSMS(context.Background(), "+2348000000000", "hi", "AcmeNG"); err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	json.Unmarshal(f.body, &payload) //nolint:errcheck
	if payload["from"] != "AcmeNG" {
		t.Fatalf("sender_id override not applied: %s", f.body)
	}
}

func TestAfricasTalking(t *testing.T) {
	var last *fakeProvider
	runCase(t, func(baseURL string, f *fakeProvider) error {
		last = f
		p := &AfricasTalking{Client: testClient("africastalking"), BaseURL: baseURL,
			APIKey: "at-key", Username: "sandbox", From: "ACME"}
		_, _, err := p.SendSMS(context.Background(), "+2348012345678", "hello", "")
		return err
	})

	f := last
	if f.path != "/version1/messaging" {
		t.Fatalf("unexpected path %q", f.path)
	}
	if f.apiKeyHdr != "at-key" {
		t.Fatalf("missing apiKey header, got %q", f.apiKeyHdr)
	}
	if ct := f.contentType; !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		t.Fatalf("expected form-encoded body, got %q", ct)
	}
	body := string(f.body)
	for _, want := range []string{"username=sandbox", "to=%2B2348012345678", "message=hello", "from=ACME"} {
		if !strings.Contains(body, want) {
			t.Fatalf("form body missing %q: %s", want, body)
		}
	}
}

func TestWhatsApp(t *testing.T) {
	var last *fakeProvider
	runCase(t, func(baseURL string, f *fakeProvider) error {
		last = f
		p := &WhatsApp{Client: testClient("whatsapp"), BaseURL: baseURL,
			Token: "wa-token", PhoneNumberID: "1234567890"}
		_, _, err := p.SendMessage(context.Background(), "2348012345678", "hello", "")
		return err
	})

	f := last
	if f.path != "/1234567890/messages" {
		t.Fatalf("unexpected path %q", f.path)
	}
	if f.authHeader != "Bearer wa-token" {
		t.Fatalf("missing bearer token, got %q", f.authHeader)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.body, &payload); err != nil {
		t.Fatalf("decode whatsapp payload: %v", err)
	}
	if payload["messaging_product"] != "whatsapp" || payload["type"] != "text" || payload["to"] != "2348012345678" {
		t.Fatalf("bad free-form payload: %s", f.body)
	}
	text, ok := payload["text"].(map[string]any)
	if !ok || text["body"] != "hello" {
		t.Fatalf("bad text body: %s", f.body)
	}
}

func TestWhatsAppTemplate(t *testing.T) {
	f := &fakeProvider{t: t, statuses: []int{200}}
	srv := f.server()
	defer srv.Close()
	p := &WhatsApp{Client: testClient("whatsapp"), BaseURL: srv.URL,
		Token: "tok", PhoneNumberID: "999"}
	if _, _, err := p.SendMessage(context.Background(), "2348012345678", "", "booking_confirmation"); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	json.Unmarshal(f.body, &payload) //nolint:errcheck
	if payload["type"] != "template" {
		t.Fatalf("expected template message: %s", f.body)
	}
	tpl, ok := payload["template"].(map[string]any)
	if !ok || tpl["name"] != "booking_confirmation" {
		t.Fatalf("bad template payload: %s", f.body)
	}
}

func TestMetricsRecorded(t *testing.T) {
	reg := metrics.New()
	c := NewClient("termii", reg, zap.NewNop())
	c.sleep = func(context.Context, int) {}

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer okSrv.Close()
	p := &Termii{Client: c, BaseURL: okSrv.URL, APIKey: "k"}
	if _, _, err := p.SendSMS(context.Background(), "+2348000000000", "hi", ""); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	reg.Render(&sb)
	if !strings.Contains(sb.String(), `messaging_gateway_sends_total{provider="termii",result="success"} 1`) {
		t.Fatalf("missing success counter:\n%s", sb.String())
	}
}
