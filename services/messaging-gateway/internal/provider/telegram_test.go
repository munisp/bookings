package provider

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTelegram(t *testing.T) {
	var last *fakeProvider
	runCase(t, func(baseURL string, f *fakeProvider) error {
		last = f
		p := &Telegram{Client: testClient("telegram"), BaseURL: baseURL, Token: "123456:ABC-def"}
		_, _, err := p.SendMessage(context.Background(), "42424242", "hello")
		return err
	})

	f := last
	if f.path != "/bot123456:ABC-def/sendMessage" {
		t.Fatalf("unexpected path %q", f.path)
	}
	if f.authHeader != "" {
		t.Fatalf("bot API must not send an Authorization header, got %q", f.authHeader)
	}
	var payload map[string]any
	if err := json.Unmarshal(f.body, &payload); err != nil {
		t.Fatalf("decode telegram payload: %v", err)
	}
	if payload["chat_id"] != "42424242" || payload["text"] != "hello" {
		t.Fatalf("bad sendMessage payload: %s", f.body)
	}
	if pm, ok := payload["parse_mode"]; !ok || pm != "" {
		t.Fatalf("plain text only: parse_mode must be present and empty: %s", f.body)
	}
}

func TestTelegramConfigured(t *testing.T) {
	p := &Telegram{}
	if p.Configured() {
		t.Fatal("empty token must not be configured")
	}
	p.Token = "123456:ABC-def"
	if !p.Configured() {
		t.Fatal("token present must be configured")
	}
}

// 4xx → provider.Error classified as a client error (no retry) — asserted
// via runCase in TestTelegram; here the concrete error shape is pinned.
func TestTelegramClientErrorShape(t *testing.T) {
	f := &fakeProvider{t: t, statuses: []int{400}}
	srv := f.server()
	defer srv.Close()
	p := &Telegram{Client: testClient("telegram"), BaseURL: srv.URL, Token: "tok"}
	_, _, err := p.SendMessage(context.Background(), "42424242", "hello")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *provider.Error, got %T (%v)", err, err)
	}
	if pe.StatusCode != 400 {
		t.Fatalf("expected status 400 in provider.Error, got %d", pe.StatusCode)
	}
}
