package activities

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripFunc lets us stub *http.Client without network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestS3Put_SignsAndUploads(t *testing.T) {
	var got *http.Request
	var gotBody []byte
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}

	err := s3Put(context.Background(), hc, "http://minio:9000", "us-east-1",
		"exports", "t-1/bundle.json", []byte(`{"ok":true}`), "minioadmin", "minioadmin")
	if err != nil {
		t.Fatalf("s3Put: %v", err)
	}
	if got.Method != http.MethodPut {
		t.Fatalf("method = %s, want PUT", got.Method)
	}
	if got.URL.String() != "http://minio:9000/exports/t-1/bundle.json" {
		t.Fatalf("url = %s", got.URL.String())
	}
	auth := got.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=minioadmin/") {
		t.Fatalf("bad Authorization header: %q", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("missing signed headers: %q", auth)
	}
	if got.Header.Get("X-Amz-Content-Sha256") == "" || got.Header.Get("X-Amz-Date") == "" {
		t.Fatal("missing x-amz headers")
	}
	if string(gotBody) != `{"ok":true}` {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestS3Put_Errors(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("AccessDenied"))}, nil
	})}
	if err := s3Put(context.Background(), hc, "http://minio:9000", "us-east-1",
		"exports", "k.json", []byte("{}"), "a", "b"); err == nil {
		t.Fatal("expected error on 403")
	}
	if err := s3Put(context.Background(), hc, "http://minio:9000", "us-east-1",
		"exports", "../evil", []byte("{}"), "a", "b"); err == nil {
		t.Fatal("expected error on path-traversal key")
	}
	if err := s3Put(context.Background(), hc, "http://minio:9000", "us-east-1",
		"exports", "k.json", []byte("{}"), "", ""); err == nil {
		t.Fatal("expected error on empty credentials")
	}
}

func TestValidateS3Key(t *testing.T) {
	for _, ok := range []string{"t-1/20250101T000000Z-ab12cd34.json", "a/b_c-d.e"} {
		if err := validateS3Key(ok); err != nil {
			t.Fatalf("key %q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/abs", "a/../b", "sp ace", "semi;colon"} {
		if err := validateS3Key(bad); err == nil {
			t.Fatalf("key %q should be invalid", bad)
		}
	}
}
