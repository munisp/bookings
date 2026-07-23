package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The geocoder hits Nominatim /search with a descriptive User-Agent,
// parses the first result, and caches by address hash so a repeated lookup
// never hits the API twice.
func TestGeocoderLookupAndCache(t *testing.T) {
	var calls atomic.Int32
	var sawUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		sawUA = r.Header.Get("User-Agent")
		if r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		w.Write([]byte(`[{"lat":"6.5244","lon":"3.3792","display_name":"Lagos"}]`))
	}))
	defer srv.Close()

	g := NewGeocoder(true, srv.URL)
	g.minGap = time.Millisecond // keep the test fast; production default is 1s

	res, err := g.Geocode(context.Background(), "  1 Marina Road, Lagos ")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Found || res.Lat != 6.5244 || res.Lng != 3.3792 {
		t.Fatalf("result = %+v", res)
	}
	if sawUA == "" || sawUA == "Go-http-client/1.1" {
		t.Fatalf("descriptive User-Agent required by Nominatim policy, got %q", sawUA)
	}

	// Same address, differently normalized → cache hit, no second call.
	res2, err := g.Geocode(context.Background(), "1 marina road,   lagos")
	if err != nil {
		t.Fatal(err)
	}
	if res2 != res {
		t.Fatalf("cached result = %+v, want %+v", res2, res)
	}
	if calls.Load() != 1 {
		t.Fatalf("nominatim calls = %d, want 1 (address-hash cache)", calls.Load())
	}
}

// Disabled hook and unknown addresses return found=false without error.
func TestGeocoderDisabledAndMiss(t *testing.T) {
	off := NewGeocoder(false, "http://127.0.0.1:1")
	if res, err := off.Geocode(context.Background(), "anywhere"); err != nil || res.Found {
		t.Fatalf("disabled geocoder = %+v, %v", res, err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	g := NewGeocoder(true, srv.URL)
	g.minGap = time.Millisecond
	res, err := g.Geocode(context.Background(), "no such place")
	if err != nil || res.Found {
		t.Fatalf("miss = %+v, %v", res, err)
	}
}

// AddressHash normalizes case/whitespace.
func TestAddressHashNormalization(t *testing.T) {
	if AddressHash("  1 Marina Road, Lagos ") != AddressHash("1 marina road,   lagos") {
		t.Fatal("equivalent addresses must share a cache key")
	}
	if AddressHash("a") == AddressHash("b") {
		t.Fatal("distinct addresses must not share a cache key")
	}
}
