package bookingops

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/events"
	"github.com/opendesk/booking-service/internal/store"
)

// UsageMetricBooking is the metered unit emitted per booking lifecycle event
// (Wave 5 #9). The analytics pipeline aggregates these into gold.usage_daily.
const UsageMetricBooking = "booking"

// MarshalUsageRecord builds the CloudEvents payload for one usage record on
// topic opendesk.usage.events:
//
//	{type: com.opendesk.usage.UsageRecord,
//	 data: {tenant_id, metric: "booking", value: 1, ts,
//	        meta: {booking_id, offering_id, price_cents}}}
//
// Emitted on BookingCreated and BookingConfirmed as an extra outbox row in
// the same transaction as the booking mutation, so metering can never drift
// from the booking ledger (at-least-once, like every outbox event).
func MarshalUsageRecord(tenantSlug string, tenantID, bookingID uuid.UUID, o store.Offering) ([]byte, error) {
	evt := events.New("booking-service", "com.opendesk.usage.UsageRecord", tenantSlug, tenantID.String(), map[string]any{
		"tenant_id": tenantID.String(),
		"metric":    UsageMetricBooking,
		"value":     1,
		"ts":        time.Now().UTC(),
		"meta": map[string]any{
			"booking_id":  bookingID.String(),
			"offering_id": o.ID.String(),
			"price_cents": o.PriceCents,
		},
	})
	return json.Marshal(evt)
}

// UsageExtra returns the extra outbox row for usage metering, or nil when
// the usage topic is not configured (metering disabled).
func (s *Service) UsageExtra(tenantSlug string, tenantID, bookingID uuid.UUID, o store.Offering) []store.ExtraOutbox {
	if s.UsageTopic == "" {
		return nil
	}
	payload, err := MarshalUsageRecord(tenantSlug, tenantID, bookingID, o)
	if err != nil {
		s.Logger.Warn("usage record marshal failed; skipping metering")
		return nil
	}
	return []store.ExtraOutbox{{Topic: s.UsageTopic, Payload: payload}}
}
