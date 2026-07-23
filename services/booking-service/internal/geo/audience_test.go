package geo

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testPolygon(t *testing.T) json.RawMessage {
	t.Helper()
	norm, err := ValidatePolygonGeoJSON(json.RawMessage(
		`{"type":"Polygon","coordinates":[[[3.37,6.52],[3.39,6.52],[3.39,6.53],[3.37,6.53],[3.37,6.52]]]}`))
	if err != nil {
		t.Fatal(err)
	}
	return norm
}

// Polygon targets compile to ST_Within with the GeoJSON bound as a
// parameter (never interpolated into the query text).
func TestBuildAudienceQueryPolygon(t *testing.T) {
	tenantID := uuid.New()
	poly := testPolygon(t)

	q, args, err := BuildAudienceCountQuery(tenantID, Target{Polygon: poly})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "ST_Within(cl.geom, ST_GeomFromGeoJSON($2)::geography)") {
		t.Fatalf("count query missing ST_Within: %s", q)
	}
	if !strings.Contains(q, "cl.tenant_id=$1") {
		t.Fatalf("count query missing tenant scope: %s", q)
	}
	if len(args) != 2 || args[0] != tenantID || args[1] != string(poly) {
		t.Fatalf("args = %v", args)
	}

	sq, sargs, err := BuildAudienceSampleQuery(tenantID, Target{Polygon: poly}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sq, "JOIN contacts") || !strings.Contains(sq, "LIMIT $3") {
		t.Fatalf("sample query malformed: %s", sq)
	}
	if len(sargs) != 3 || sargs[2] != 10 {
		t.Fatalf("sample args = %v", sargs)
	}
}

// Center+radius targets compile to ST_DWithin with lng/lat/radius bound in
// that order (ST_MakePoint takes x=lng, y=lat).
func TestBuildAudienceQueryRadius(t *testing.T) {
	tenantID := uuid.New()
	target := Target{HasCenter: true, CenterLat: 6.5244, CenterLng: 3.3792, RadiusM: 1500}

	q, args, err := BuildAudienceCountQuery(tenantID, target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "ST_DWithin(cl.geom, ST_SetSRID(ST_MakePoint($2, $3), 4326)::geography, $4)") {
		t.Fatalf("count query missing ST_DWithin: %s", q)
	}
	if len(args) != 4 || args[1] != 3.3792 || args[2] != 6.5244 || args[3] != 1500.0 {
		t.Fatalf("args = %v (want tenant, lng, lat, radius)", args)
	}
}

// Exactly one selector: polygon XOR center+radius_m.
func TestBuildAudienceQueryTargetValidation(t *testing.T) {
	tenantID := uuid.New()
	poly := testPolygon(t)

	if _, _, err := BuildAudienceCountQuery(tenantID, Target{}); err == nil {
		t.Fatal("no selector should fail")
	}
	if _, _, err := BuildAudienceCountQuery(tenantID, Target{Polygon: poly, HasCenter: true, CenterLat: 6, CenterLng: 3, RadiusM: 100}); err == nil {
		t.Fatal("both selectors should fail")
	}
	if _, _, err := BuildAudienceCountQuery(tenantID, Target{HasCenter: true, CenterLat: 6, CenterLng: 3, RadiusM: 0}); err == nil {
		t.Fatal("zero radius should fail")
	}
	if _, _, err := BuildAudienceCountQuery(tenantID, Target{HasCenter: true, CenterLat: 6, CenterLng: 3, RadiusM: 500_000}); err == nil {
		t.Fatal("radius above the 200km cap should fail")
	}
	if _, _, err := BuildAudienceCountQuery(tenantID, Target{HasCenter: true, CenterLat: 95, CenterLng: 3, RadiusM: 100}); err == nil {
		t.Fatal("out-of-bbox center should fail")
	}
}

// The summary points query is booking-joined, tenant-scoped, capped at
// 5000, and binds the from/to/offering filters as parameters.
func TestBuildLocationPointsQuery(t *testing.T) {
	tenantID := uuid.New()
	from := time.Now().Add(-24 * time.Hour)
	to := time.Now()
	offeringID := uuid.New()

	q, args := BuildLocationPointsQuery(tenantID, SummaryFilter{From: &from, To: &to, OfferingID: &offeringID})
	for _, frag := range []string{"JOIN bookings", "cl.tenant_id = $1", "b.starts_at >= $2", "b.starts_at <= $3", "b.offering_id = $4", "LIMIT 5000"} {
		if !strings.Contains(q, frag) {
			t.Fatalf("points query missing %q: %s", frag, q)
		}
	}
	if len(args) != 4 || args[0] != tenantID || args[1] != from || args[2] != to || args[3] != offeringID {
		t.Fatalf("args = %v", args)
	}

	// No filters: only the tenant arg.
	q2, args2 := BuildLocationPointsQuery(tenantID, SummaryFilter{})
	if strings.Contains(q2, "starts_at >=") || len(args2) != 1 {
		t.Fatalf("unfiltered query = %s args=%v", q2, args2)
	}
}

// The cluster query aggregates the same joined set via ST_SnapToGrid.
func TestBuildLocationClustersQuery(t *testing.T) {
	tenantID := uuid.New()
	q, args := BuildLocationClustersQuery(tenantID, SummaryFilter{})
	for _, frag := range []string{"ST_SnapToGrid", "ST_Centroid", "COUNT(*)", "GROUP BY", "JOIN bookings", "cl.tenant_id = $1"} {
		if !strings.Contains(q, frag) {
			t.Fatalf("cluster query missing %q: %s", frag, q)
		}
	}
	if len(args) != 1 || args[0] != tenantID {
		t.Fatalf("args = %v", args)
	}
}
