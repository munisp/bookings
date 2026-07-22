package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSignatureHeader(t *testing.T) {
	body := []byte(`{"specversion":"1.0","type":"com.opendesk.booking.BookingCreated"}`)
	got := SignatureHeader("whsec_test", body)

	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	// Deterministic + body-sensitive.
	if SignatureHeader("whsec_test", body) != want {
		t.Fatal("signing is not deterministic")
	}
	if SignatureHeader("whsec_test", []byte(`{}`)) == want {
		t.Fatal("different bodies produced the same signature")
	}
	if SignatureHeader("other-secret", body) == want {
		t.Fatal("different secrets produced the same signature")
	}
	// Empty secret → unsigned delivery (only allowed when signing is not required).
	if SignatureHeader("", body) != "" {
		t.Fatal("empty secret must produce an empty signature")
	}
}

func TestEventMatches(t *testing.T) {
	const created = "com.opendesk.booking.BookingCreated"
	cases := []struct {
		filter []string
		event  string
		want   bool
	}{
		{[]string{created}, created, true},
		{[]string{"com.opendesk.booking.BookingCancelled"}, created, false},
		{[]string{"*"}, created, true},
		{[]string{"com.opendesk.booking.*"}, created, true},
		{[]string{"com.opendesk.booking.*"}, "com.opendesk.conversation.SessionEnded", false},
		{[]string{"com.opendesk.conversation.*"}, "com.opendesk.conversation.SessionEnded", true},
		{[]string{"com.opendesk.payments.*", created}, created, true},
		{[]string{}, created, false},
		{nil, created, false},
	}
	for i, c := range cases {
		if got := EventMatches(c.filter, c.event); got != c.want {
			t.Fatalf("case %d: EventMatches(%v, %q) = %v, want %v", i, c.filter, c.event, got, c.want)
		}
	}
}
