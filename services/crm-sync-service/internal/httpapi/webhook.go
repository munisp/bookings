// HMAC-SHA256 verification for Twenty webhook intake (SPEC-CRM §B).
package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// VerifySignature checks the X-Twenty-Webhook-Signature header against the
// raw request body using the shared secret. The header may carry a hex or
// base64 encoded digest, optionally prefixed with "sha256=".
func VerifySignature(secret string, body []byte, header string) bool {
	if secret == "" || header == "" {
		return false
	}
	header = strings.TrimSpace(strings.TrimPrefix(header, "sha256="))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)

	if got, err := hex.DecodeString(header); err == nil {
		return subtle.ConstantTimeCompare(got, want) == 1
	}
	if got, err := base64.StdEncoding.DecodeString(header); err == nil {
		return subtle.ConstantTimeCompare(got, want) == 1
	}
	return false
}
