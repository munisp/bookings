package geo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Optional Nominatim geocoding hook (SPEC-W8 A2). OFF by default
// (GEOCODE_ENABLED=false); when enabled, booking/contact address strings
// can be resolved to coordinates with source=`geocode`.
//
// Nominatim usage-policy discipline, all enforced client-side:
//   - max 1 request/second (process-local limiter);
//   - a descriptive User-Agent header on every request;
//   - 5s HTTP timeout;
//   - results cached by address hash so repeated lookups of the same
//     address never hit the API twice.

// geocodeUserAgent identifies this deployment per the Nominatim policy.
const geocodeUserAgent = "OpenDesk booking-service geo-campaigns (https://github.com/opendesk; contact: ops@opendesk.local)"

// Geocoder is a rate-limited, caching Nominatim client.
type Geocoder struct {
	enabled bool
	baseURL string
	client  *http.Client

	mu     sync.Mutex
	last   time.Time     // last API call start (1 req/s spacing)
	minGap time.Duration // 1s for Nominatim; tests may shrink it
	cache  map[string]*GeocodeResult
}

// GeocodeResult is one resolved (or negatively cached) address lookup.
type GeocodeResult struct {
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Found bool    `json:"found"`
}

// NewGeocoder builds the client; enabled=false disables all lookups.
func NewGeocoder(enabled bool, baseURL string) *Geocoder {
	if baseURL == "" {
		baseURL = "https://nominatim.openstreetmap.org"
	}
	return &Geocoder{
		enabled: enabled,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
		minGap:  time.Second,
		cache:   map[string]*GeocodeResult{},
	}
}

// Enabled reports whether the hook is active.
func (g *Geocoder) Enabled() bool { return g != nil && g.enabled }

// AddressHash is the cache key of an address string (normalized: trimmed,
// lower-cased, whitespace-collapsed, sha256).
func AddressHash(address string) string {
	norm := strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(address))), " ")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// Geocode resolves an address to coordinates. found=false (no error) when
// the hook is disabled, the address is empty, or Nominatim has no match;
// both hits and misses are cached by address hash.
func (g *Geocoder) Geocode(ctx context.Context, address string) (GeocodeResult, error) {
	if !g.Enabled() || strings.TrimSpace(address) == "" {
		return GeocodeResult{}, nil
	}
	key := AddressHash(address)
	g.mu.Lock()
	if hit, ok := g.cache[key]; ok {
		res := *hit
		g.mu.Unlock()
		return res, nil
	}
	// 1 req/s: wait out the remainder of the gap since the last call.
	if wait := g.minGap - time.Since(g.last); wait > 0 {
		g.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return GeocodeResult{}, ctx.Err()
		case <-timer.C:
		}
		g.mu.Lock()
	}
	g.last = time.Now()
	g.mu.Unlock()

	res, err := g.lookup(ctx, address)
	if err != nil {
		return GeocodeResult{}, err
	}
	g.mu.Lock()
	g.cache[key] = &res
	g.mu.Unlock()
	return res, nil
}

// nominatimResult mirrors the subset of the /search response we use.
type nominatimResult struct {
	Lat string `json:"lat"`
	Lon string `json:"lon"`
}

func (g *Geocoder) lookup(ctx context.Context, address string) (GeocodeResult, error) {
	u := g.baseURL + "/search?format=json&limit=1&q=" + url.QueryEscape(address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return GeocodeResult{}, err
	}
	req.Header.Set("User-Agent", geocodeUserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return GeocodeResult{}, fmt.Errorf("nominatim request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return GeocodeResult{}, fmt.Errorf("nominatim status %d", resp.StatusCode)
	}
	var results []nominatimResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return GeocodeResult{}, fmt.Errorf("nominatim decode: %w", err)
	}
	if len(results) == 0 {
		return GeocodeResult{Found: false}, nil
	}
	lat, err1 := strconv.ParseFloat(results[0].Lat, 64)
	lng, err2 := strconv.ParseFloat(results[0].Lon, 64)
	if err1 != nil || err2 != nil {
		return GeocodeResult{}, fmt.Errorf("nominatim returned unparseable coordinates")
	}
	if err := ValidateLatLng(lat, lng); err != nil {
		return GeocodeResult{}, fmt.Errorf("nominatim coordinates out of range: %w", err)
	}
	return GeocodeResult{Lat: lat, Lng: lng, Found: true}, nil
}
