package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func signHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignatureHex(t *testing.T) {
	body := []byte(`{"event":"person.created","id":"1"}`)
	sig := signHex("s3cret", body)
	if !VerifySignature("s3cret", body, sig) {
		t.Fatal("valid hex signature rejected")
	}
	if !VerifySignature("s3cret", body, "sha256="+sig) {
		t.Fatal("valid sha256=-prefixed signature rejected")
	}
}

func TestVerifySignatureBase64(t *testing.T) {
	body := []byte(`{"event":"task.updated"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	if !VerifySignature("s3cret", body, base64.StdEncoding.EncodeToString(mac.Sum(nil))) {
		t.Fatal("valid base64 signature rejected")
	}
}

func TestVerifySignatureRejects(t *testing.T) {
	body := []byte(`{"event":"person.created"}`)
	good := signHex("s3cret", body)
	cases := []struct {
		name, secret, header string
		b                    []byte
	}{
		{"wrong secret", "other", good, body},
		{"tampered body", "s3cret", good, []byte(`{"event":"person.deleted"}`)},
		{"empty header", "s3cret", "", body},
		{"garbage header", "s3cret", "not-a-signature", body},
		{"empty secret", "", good, body},
	}
	for _, c := range cases {
		if VerifySignature(c.secret, c.b, c.header) {
			t.Errorf("%s: invalid signature accepted", c.name)
		}
	}
}
