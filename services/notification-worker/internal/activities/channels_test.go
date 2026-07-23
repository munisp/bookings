package activities

import "testing"

func TestChannelRouterResolution(t *testing.T) {
	r, err := NewChannelRouter("email:smtp,sms:twilio",
		`{"acme-ng": {"sms": "termii"}, "lagos-clinic": {"sms": "africastalking"}, "default": {"sms": "twilio"}}`)
	if err != nil {
		t.Fatalf("NewChannelRouter: %v", err)
	}

	cases := []struct {
		name    string
		channel string
		tenant  string
		want    string
	}{
		{"default sms", "sms", "unknown-tenant", "twilio"},
		{"default email", "email", "unknown-tenant", "smtp"},
		{"empty tenant falls back to default", "sms", "", "twilio"},
		{"tenant override sms termii", "sms", "acme-ng", "termii"},
		{"tenant override sms africastalking", "sms", "lagos-clinic", "africastalking"},
		{"tenant override does not leak into email", "email", "acme-ng", "smtp"},
		{"unknown channel resolves empty", "push", "acme-ng", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.Provider(tc.channel, tc.tenant); got != tc.want {
				t.Fatalf("Provider(%q, %q) = %q, want %q", tc.channel, tc.tenant, got, tc.want)
			}
		})
	}
}

func TestChannelRouterDefaultKeyOverridesFleet(t *testing.T) {
	r, err := NewChannelRouter("email:smtp,sms:twilio", `{"default": {"sms": "africastalking"}}`)
	if err != nil {
		t.Fatalf("NewChannelRouter: %v", err)
	}
	if got := r.Provider("sms", "some-tenant"); got != "africastalking" {
		t.Fatalf("default key must override fleet default, got %q", got)
	}
	if got := r.Provider("email", "some-tenant"); got != "smtp" {
		t.Fatalf("email default must stay smtp, got %q", got)
	}
}

func TestChannelRouterBuiltinsWhenEmpty(t *testing.T) {
	r, err := NewChannelRouter("", "")
	if err != nil {
		t.Fatalf("NewChannelRouter: %v", err)
	}
	if got := r.Provider("sms", "acme-ng"); got != "twilio" {
		t.Fatalf("empty spec must default sms to twilio, got %q", got)
	}
	if got := r.Provider("email", ""); got != "smtp" {
		t.Fatalf("empty spec must default email to smtp, got %q", got)
	}
}

func TestChannelRouterNilSafe(t *testing.T) {
	var r *ChannelRouter
	if got := r.Provider("sms", "acme-ng"); got != "twilio" {
		t.Fatalf("nil router must fall back to twilio, got %q", got)
	}
	if got := r.Provider("email", ""); got != "smtp" {
		t.Fatalf("nil router must fall back to smtp, got %q", got)
	}
}

func TestChannelRouterInvalid(t *testing.T) {
	cases := []struct {
		name       string
		spec       string
		tenantJSON string
	}{
		{"bad spec entry", "sms", ""},
		{"unknown channel", "ussd:termii", ""},
		{"invalid sms provider", "sms:pigeon", ""},
		{"invalid email provider", "email:termii", ""},
		{"bad tenant json", "", `{not json`},
		{"bad tenant provider", "", `{"acme-ng": {"sms": "pigeon"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewChannelRouter(tc.spec, tc.tenantJSON); err == nil {
				t.Fatalf("expected error for spec=%q tenant=%q", tc.spec, tc.tenantJSON)
			}
		})
	}
}

func TestBindingName(t *testing.T) {
	a := &Activities{SMTPBinding: "bindings-smtp", TwilioBinding: "bindings-twilio"}
	cases := map[string]string{
		"smtp":           "bindings-smtp",
		"twilio":         "bindings-twilio",
		"termii":         "bindings-termii",
		"africastalking": "bindings-africastalking",
		"whatsapp":       "bindings-whatsapp",
	}
	for provider, want := range cases {
		if got := a.BindingName(provider); got != want {
			t.Fatalf("BindingName(%q) = %q, want %q", provider, got, want)
		}
	}
}
