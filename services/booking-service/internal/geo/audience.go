package geo

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Audience + summary query builders (SPEC-W8 A2). These are PURE functions
// returning parameterized SQL ($1… placeholders) plus the bound argument
// list, so they are unit-testable without a database (the repo's embedded
// Postgres has no PostGIS) and user input can never be interpolated into
// the query text — GeoJSON goes in as a bound string to
// ST_GeomFromGeoJSON, coordinates as bound floats.

// LocationSummaryLimit caps the points payload (contract: 5000).
const LocationSummaryLimit = 5000

// LocationClusterThreshold: above this many points the summary also
// returns server-side ST_SnapToGrid clusters (contract: > 500).
const LocationClusterThreshold = 500

// LocationClusterGridDeg is the ST_SnapToGrid cell size in degrees
// (~5 km at the equator) used for server-side clustering.
const LocationClusterGridDeg = 0.05

// AudienceSampleLimit caps the masked phone sample of the preview.
const AudienceSampleLimit = 10

// maxRadiusM bounds ST_DWithin targets (200 km) so a typo cannot scan the
// whole planet.
const maxRadiusM = 200_000.0

// Target is a geo audience selector: EITHER a GeoJSON Polygon/MultiPolygon
// (already validated by ValidatePolygonGeoJSON) OR a center + radius in
// meters.
type Target struct {
	Polygon   json.RawMessage
	HasCenter bool
	CenterLat float64
	CenterLng float64
	RadiusM   float64
}

// validate enforces the exactly-one-selector rule and bounds.
func (t Target) validate() error {
	hasPoly := len(t.Polygon) > 0
	if hasPoly == t.HasCenter {
		return fmt.Errorf("exactly one of polygon or center+radius_m is required")
	}
	if t.HasCenter {
		if err := ValidateLatLng(t.CenterLat, t.CenterLng); err != nil {
			return err
		}
		if t.RadiusM <= 0 || t.RadiusM > maxRadiusM {
			return fmt.Errorf("radius_m must be in (0, %v]", maxRadiusM)
		}
	}
	return nil
}

// where returns the spatial WHERE fragment and its args, with placeholders
// numbered from arg (args[$1] is always the tenant id, so pass 2).
func (t Target) where(arg int) (string, []any, error) {
	if err := t.validate(); err != nil {
		return "", nil, err
	}
	if len(t.Polygon) > 0 {
		return fmt.Sprintf(`ST_Within(cl.geom, ST_GeomFromGeoJSON($%d)::geography)`, arg),
			[]any{string(t.Polygon)}, nil
	}
	return fmt.Sprintf(`ST_DWithin(cl.geom, ST_SetSRID(ST_MakePoint($%d, $%d), 4326)::geography, $%d)`,
			arg, arg+1, arg+2),
		[]any{t.CenterLng, t.CenterLat, t.RadiusM}, nil
}

// BuildAudienceCountQuery builds the audience preview count query:
// args[0] is the tenant id, followed by the target args.
func BuildAudienceCountQuery(tenantID uuid.UUID, t Target) (string, []any, error) {
	where, args, err := t.where(2)
	if err != nil {
		return "", nil, err
	}
	q := `SELECT COUNT(*) FROM contact_locations cl WHERE cl.tenant_id=$1 AND ` + where
	return q, append([]any{tenantID}, args...), nil
}

// BuildAudienceSampleQuery builds the masked-sample query (contact id +
// raw phone; masking happens at the handler boundary).
func BuildAudienceSampleQuery(tenantID uuid.UUID, t Target, limit int) (string, []any, error) {
	if limit <= 0 || limit > AudienceSampleLimit {
		limit = AudienceSampleLimit
	}
	where, args, err := t.where(2)
	if err != nil {
		return "", nil, err
	}
	n := len(args) + 2
	q := `SELECT cl.contact_id, c.phone
	      FROM contact_locations cl
	      JOIN contacts c ON c.tenant_id = cl.tenant_id AND c.id = cl.contact_id
	      WHERE cl.tenant_id=$1 AND ` + where +
		fmt.Sprintf(` ORDER BY cl.updated_at DESC LIMIT $%d`, n)
	return q, append(append([]any{tenantID}, args...), limit), nil
}

// SummaryFilter narrows the booking-joined location summary.
type SummaryFilter struct {
	From       *time.Time
	To         *time.Time
	OfferingID *uuid.UUID
}

// summaryJoinWhere renders the shared FROM/WHERE of the summary queries:
// booking-joined contact points for one tenant. args[0] is the tenant id;
// returns the clause and args (tenant first).
func summaryJoinWhere(tenantID uuid.UUID, f SummaryFilter) (string, []any) {
	clause := ` FROM contact_locations cl
	      JOIN bookings b ON b.tenant_id = cl.tenant_id AND b.contact_id = cl.contact_id
	      WHERE cl.tenant_id = $1`
	args := []any{tenantID}
	n := 1
	if f.From != nil {
		n++
		clause += fmt.Sprintf(` AND b.starts_at >= $%d`, n)
		args = append(args, *f.From)
	}
	if f.To != nil {
		n++
		clause += fmt.Sprintf(` AND b.starts_at <= $%d`, n)
		args = append(args, *f.To)
	}
	if f.OfferingID != nil {
		n++
		clause += fmt.Sprintf(` AND b.offering_id = $%d`, n)
		args = append(args, *f.OfferingID)
	}
	return clause, args
}

// BuildLocationPointsQuery builds the summary points query (capped at
// LocationSummaryLimit).
func BuildLocationPointsQuery(tenantID uuid.UUID, f SummaryFilter) (string, []any) {
	join, args := summaryJoinWhere(tenantID, f)
	q := `SELECT ST_Y(cl.geom::geometry) AS lat, ST_X(cl.geom::geometry) AS lng,
	             b.id, b.offering_id, b.starts_at` + join +
		fmt.Sprintf(` ORDER BY b.starts_at DESC LIMIT %d`, LocationSummaryLimit)
	return q, args
}

// BuildLocationClustersQuery builds the server-side ST_SnapToGrid cluster
// query over the same booking-joined set; the centroid of each non-empty
// grid cell + its point count comes back.
func BuildLocationClustersQuery(tenantID uuid.UUID, f SummaryFilter) (string, []any) {
	join, args := summaryJoinWhere(tenantID, f)
	q := `SELECT ST_Y(ST_Centroid(ST_Collect(cl.geom::geometry))) AS lat,
	             ST_X(ST_Centroid(ST_Collect(cl.geom::geometry))) AS lng,
	             COUNT(*)::int AS count` + join +
		fmt.Sprintf(` GROUP BY ST_SnapToGrid(cl.geom::geometry, %v)`, LocationClusterGridDeg)
	return q, args
}
