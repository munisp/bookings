package geo

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/events"
)

// The usage record is a CloudEvents envelope with metric
// geo_campaign_message, value 1, and campaign/contact metadata — one per
// recipient, on opendesk.usage.events.
func TestMarshalGeoUsageRecord(t *testing.T) {
	tenantID, campaignID, contactID := uuid.New(), uuid.New(), uuid.New()

	payload, err := MarshalGeoUsageRecord("acme", tenantID, campaignID, contactID)
	if err != nil {
		t.Fatal(err)
	}
	var evt events.CloudEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		t.Fatalf("payload is not a CloudEvent: %v", err)
	}
	if evt.Type != "com.opendesk.usage.UsageRecord" {
		t.Fatalf("type = %q", evt.Type)
	}
	if evt.Source != "booking-service" || evt.Subject != "acme" || evt.TenantID != tenantID.String() {
		t.Fatalf("envelope = %+v", evt)
	}
	if evt.Data["tenant_id"] != tenantID.String() {
		t.Fatalf("data.tenant_id = %v", evt.Data["tenant_id"])
	}
	if evt.Data["metric"] != UsageMetricGeoCampaignMessage {
		t.Fatalf("metric = %v, want geo_campaign_message", evt.Data["metric"])
	}
	if evt.Data["value"].(float64) != 1 {
		t.Fatalf("value = %v, want 1 per recipient", evt.Data["value"])
	}
	meta, ok := evt.Data["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta missing: %v", evt.Data)
	}
	if meta["campaign_id"] != campaignID.String() || meta["contact_id"] != contactID.String() {
		t.Fatalf("meta = %v", meta)
	}
}
