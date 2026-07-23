package geo

import (
	"encoding/json"
	"testing"
)

func TestValidatePolygonGeoJSONValidPolygon(t *testing.T) {
	raw := json.RawMessage(`{"type":"Polygon","coordinates":[[[3.37,6.52],[3.39,6.52],[3.39,6.53],[3.37,6.53],[3.37,6.52]]]}`)
	norm, err := ValidatePolygonGeoJSON(raw)
	if err != nil {
		t.Fatalf("valid polygon rejected: %v", err)
	}
	var g struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(norm, &g); err != nil || g.Type != "Polygon" {
		t.Fatalf("normalized geojson = %s, err=%v", norm, err)
	}
}

func TestValidatePolygonGeoJSONValidMultiPolygon(t *testing.T) {
	raw := json.RawMessage(`{"type":"MultiPolygon","coordinates":[
		[[[3.37,6.52],[3.39,6.52],[3.39,6.53],[3.37,6.52]]],
		[[[-0.13,51.50],[-0.12,51.50],[-0.12,51.51],[-0.13,51.50]]]]}`)
	if _, err := ValidatePolygonGeoJSON(raw); err != nil {
		t.Fatalf("valid multipolygon rejected: %v", err)
	}
}

func TestValidatePolygonGeoJSONRejects(t *testing.T) {
	cases := map[string]string{
		"empty":             ``,
		"not json":          `{oops`,
		"wrong type":        `{"type":"Point","coordinates":[3.37,6.52]}`,
		"feature not geom":  `{"type":"Feature","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}}`,
		"too few positions": `{"type":"Polygon","coordinates":[[[0,0],[1,0],[0,0]]]}`,
		"unclosed ring":     `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,1]]]}`,
		"lat out of bbox":   `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,91],[0,0]]]}`,
		"lng out of bbox":   `{"type":"Polygon","coordinates":[[[0,0],[181,0],[1,1],[0,0]]]}`,
		"scalar coords":     `{"type":"Polygon","coordinates":5}`,
		"empty multipoly":   `{"type":"MultiPolygon","coordinates":[]}`,
		"empty polygon":     `{"type":"Polygon","coordinates":[]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidatePolygonGeoJSON(json.RawMessage(raw)); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestValidateLatLngBBox(t *testing.T) {
	if err := ValidateLatLng(6.5244, 3.3792); err != nil {
		t.Fatalf("Lagos rejected: %v", err)
	}
	for _, ll := range [][2]float64{{-90.1, 0}, {90.1, 0}, {0, -180.1}, {0, 180.1}} {
		if err := ValidateLatLng(ll[0], ll[1]); err == nil {
			t.Fatalf("(%v,%v) should be out of range", ll[0], ll[1])
		}
	}
	// Edges are valid.
	if err := ValidateLatLng(-90, -180); err != nil {
		t.Fatalf("bbox corner rejected: %v", err)
	}
	if err := ValidateLatLng(90, 180); err != nil {
		t.Fatalf("bbox corner rejected: %v", err)
	}
}
