// Package geo implements the SPEC-W8 Part A geospatial feature set of
// booking-service: GeoJSON validation, audience/summary SQL builders, the
// optional Nominatim geocoding hook, the geo HTTP handlers and the
// GeoCampaignWorkflow with its activities.
package geo

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// GeoJSON validation (SPEC-W8 A2): service areas and campaign targets
// arrive as GeoJSON Polygon/MultiPolygon and are validated structurally in
// Go BEFORE hitting ST_GeomFromGeoJSON, so clients get actionable 400s
// instead of raw PostGIS errors. The validated (re-serialized, compact)
// document is what gets bound as a query parameter — user input is never
// interpolated into SQL.

// Coordinate bounds (WGS84).
const (
	minLat, maxLat = -90.0, 90.0
	minLng, maxLng = -180.0, 180.0
)

// ValidateLatLng range-checks a WGS84 coordinate pair.
func ValidateLatLng(lat, lng float64) error {
	if lat < minLat || lat > maxLat {
		return fmt.Errorf("lat %v out of range [-90, 90]", lat)
	}
	if lng < minLng || lng > maxLng {
		return fmt.Errorf("lng %v out of range [-180, 180]", lng)
	}
	return nil
}

// ValidatePolygonGeoJSON parses and validates a GeoJSON Polygon or
// MultiPolygon geometry object:
//
//   - type must be "Polygon" or "MultiPolygon";
//   - every linear ring has >= 4 positions and is closed (first == last);
//   - every position is [lng, lat] (optional extra ordinates ignored)
//     inside the WGS84 bbox.
//
// Returns the compact re-serialized geometry on success.
func ValidatePolygonGeoJSON(raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("geojson is required")
	}
	var g struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("invalid geojson: %w", err)
	}
	switch g.Type {
	case "Polygon":
		var poly [][][]float64
		if err := json.Unmarshal(g.Coordinates, &poly); err != nil {
			return nil, fmt.Errorf("invalid Polygon coordinates: %w", err)
		}
		if err := validatePolygon(poly); err != nil {
			return nil, err
		}
	case "MultiPolygon":
		var multi [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &multi); err != nil {
			return nil, fmt.Errorf("invalid MultiPolygon coordinates: %w", err)
		}
		if len(multi) == 0 {
			return nil, fmt.Errorf("MultiPolygon must contain at least one polygon")
		}
		for i, poly := range multi {
			if err := validatePolygon(poly); err != nil {
				return nil, fmt.Errorf("polygon %d: %w", i, err)
			}
		}
	default:
		return nil, fmt.Errorf("geojson type %q not supported (want Polygon or MultiPolygon)", g.Type)
	}
	// Re-serialize compactly so downstream SQL binds a normalized document.
	out, err := json.Marshal(map[string]any{"type": g.Type, "coordinates": g.Coordinates})
	if err != nil {
		return nil, fmt.Errorf("reserialize geojson: %w", err)
	}
	return out, nil
}

// validatePolygon checks ring count, ring closure and coordinate bounds.
func validatePolygon(poly [][][]float64) error {
	if len(poly) == 0 {
		return fmt.Errorf("polygon must have at least one ring")
	}
	for i, ring := range poly {
		if err := validateRing(ring); err != nil {
			return fmt.Errorf("ring %d: %w", i, err)
		}
	}
	return nil
}

// validateRing enforces the GeoJSON linear-ring rules: >= 4 positions,
// closed, in-bounds [lng, lat] positions.
func validateRing(ring [][]float64) error {
	if len(ring) < 4 {
		return fmt.Errorf("linear ring must have at least 4 positions, got %d", len(ring))
	}
	for j, pos := range ring {
		if len(pos) < 2 {
			return fmt.Errorf("position %d must be [lng, lat]", j)
		}
		if err := ValidateLatLng(pos[1], pos[0]); err != nil {
			return fmt.Errorf("position %d: %w", j, err)
		}
	}
	first, last := ring[0], ring[len(ring)-1]
	if first[0] != last[0] || first[1] != last[1] {
		return fmt.Errorf("linear ring is not closed (first position != last)")
	}
	return nil
}
