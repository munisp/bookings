package twentyc

import (
	"net/url"
	"testing"
)

func TestExprQuotesValue(t *testing.T) {
	got := Expr("emails.primaryEmail", OpEq, "jane@example.com")
	want := `emails.primaryEmail[eq]:"jane@example.com"`
	if got != want {
		t.Fatalf("Expr = %q, want %q", got, want)
	}
}

func TestExprEscapesQuotes(t *testing.T) {
	got := Expr("name", OpEq, `O"Brien`)
	want := `name[eq]:"O\"Brien"`
	if got != want {
		t.Fatalf("Expr = %q, want %q", got, want)
	}
}

func TestPersonFilters(t *testing.T) {
	if got := PersonEmailFilter("a@b.co"); got != `emails.primaryEmail[eq]:"a@b.co"` {
		t.Fatalf("PersonEmailFilter = %q", got)
	}
	if got := PersonPhoneFilter("+15551234567"); got != `phones.primaryPhoneNumber[eq]:"+15551234567"` {
		t.Fatalf("PersonPhoneFilter = %q", got)
	}
	if got := CompanyNameFilter("Acme Salon"); got != `name[eq]:"Acme Salon"` {
		t.Fatalf("CompanyNameFilter = %q", got)
	}
}

func TestAndExpr(t *testing.T) {
	if got := AndExpr("a[eq]:1"); got != "a[eq]:1" {
		t.Fatalf("single AndExpr = %q", got)
	}
	if got := AndExpr("a[eq]:1", "b[eq]:2"); got != "and(a[eq]:1,b[eq]:2)" {
		t.Fatalf("multi AndExpr = %q", got)
	}
}

func TestQueryEncodingRoundTrip(t *testing.T) {
	q := Query(PersonEmailFilter("jane+x@example.com"), 1)
	encoded := q.Encode()
	vals, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	got := vals.Get("filter")
	want := `emails.primaryEmail[eq]:"jane+x@example.com"`
	if got != want {
		t.Fatalf("decoded filter = %q, want %q", got, want)
	}
	if vals.Get("limit") != "1" {
		t.Fatalf("limit = %q", vals.Get("limit"))
	}
}
