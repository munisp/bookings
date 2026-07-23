package geo

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/events"
)

// UsageMetricGeoCampaignMessage is the metered unit emitted per geo
// campaign recipient (SPEC-W8 A2): geo campaigns are billed per message.
const UsageMetricGeoCampaignMessage = "geo_campaign_message"

// MarshalGeoUsageRecord builds the CloudEvents payload for one usage
// record on topic opendesk.usage.events, mirroring
// bookingops.MarshalUsageRecord:
//
//	{type: com.opendesk.usage.UsageRecord,
//	 data: {tenant_id, metric: "geo_campaign_message", value: 1, ts,
//	        meta: {campaign_id, contact_id}}}
//
// One record per recipient, written as an extra outbox row in the SAME
// transaction as the campaign-send ledger row (UsageExtra pattern), so
// metering can never drift from what was actually sent.
func MarshalGeoUsageRecord(tenantSlug string, tenantID, campaignID, contactID uuid.UUID) ([]byte, error) {
	evt := events.New("booking-service", "com.opendesk.usage.UsageRecord", tenantSlug, tenantID.String(), map[string]any{
		"tenant_id": tenantID.String(),
		"metric":    UsageMetricGeoCampaignMessage,
		"value":     1,
		"ts":        time.Now().UTC(),
		"meta": map[string]any{
			"campaign_id": campaignID.String(),
			"contact_id":  contactID.String(),
		},
	})
	return json.Marshal(evt)
}
