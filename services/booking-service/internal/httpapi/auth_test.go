package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func makeToken(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}

func TestParseBearerClaims(t *testing.T) {
	tok := makeToken(map[string]any{
		"sub":          "user-1",
		"tenant_slugs": []string{"acme", "globex"},
	})
	claims := parseBearerClaims("Bearer " + tok)
	if claims.Sub != "user-1" {
		t.Fatalf("sub = %q", claims.Sub)
	}
	if !claims.hasTenant("acme") || !claims.hasTenant("globex") {
		t.Fatal("expected both tenants")
	}
	if claims.hasTenant("other") {
		t.Fatal("unexpected tenant")
	}
	if claims.firstTenant() != "acme" {
		t.Fatalf("firstTenant = %q", claims.firstTenant())
	}
}

func TestParseBearerClaimsInvalid(t *testing.T) {
	for _, h := range []string{"", "Bearer x", "Bearer a.b", "Basic abc", "Bearer a.!!!.c"} {
		claims := parseBearerClaims(h)
		if claims.Sub != "" || len(claims.TenantSlugs) != 0 {
			t.Fatalf("expected empty claims for %q, got %+v", h, claims)
		}
	}
}

func TestParseBearerClaimsEmail(t *testing.T) {
	tok := makeToken(map[string]any{"sub": "user-2", "email": "ana@acme.test"})
	claims := parseBearerClaims("Bearer " + tok)
	if claims.Email != "ana@acme.test" {
		t.Fatalf("email = %q", claims.Email)
	}
	// tokens without an email claim decode to empty (mine=true then 403s
	// or falls back to X-User-Email)
	tok2 := makeToken(map[string]any{"sub": "user-3"})
	if c := parseBearerClaims("Bearer " + tok2); c.Email != "" {
		t.Fatalf("email = %q, want empty", c.Email)
	}
}
