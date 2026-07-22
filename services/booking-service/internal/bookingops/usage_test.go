package bookingops

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/opendesk/booking-service/internal/store"
	"go.uber.org/zap"
)

// The usage record CloudEvent published to opendesk.usage.events on
// BookingCreated / BookingConfirmed (Wave 5 #9).
func TestMarshalUsageRecordShape(t *testing.T) {
	tenantID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	bookingID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	offeringID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	offering := store.Offering{ID: offeringID, PriceCents: 4200}

	raw, err := MarshalUsageRecord("acme", tenantID, bookingID, offering)
	if err != nil {
		t.Fatal(err)
	}
	var evt map[string]any
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}

	if evt["specversion"] != "1.0" {
		t.Fatalf("specversion = %v", evt["specversion"])
	}
	if evt["type"] != "com.opendesk.usage.UsageRecord" {
		t.Fatalf("type = %v", evt["type"])
	}
	if evt["source"] != "booking-service" {
		t.Fatalf("source = %v", evt["source"])
	}
	if evt["tenantid"] != tenantID.String() {
		t.Fatalf("tenantid ext = %v", evt["tenantid"])
	}
	if evt["id"] == "" || evt["time"] == "" {
		t.Fatal("envelope id/time must be set")
	}

	data, ok := evt["data"].(map[string]any)
	if !ok {
		t.Fatalf("data missing or wrong type: %T", evt["data"])
	}
	if data["tenant_id"] != tenantID.String() {
		t.Fatalf("data.tenant_id = %v", data["tenant_id"])
	}
	if data["metric"] != "booking" {
		t.Fatalf("data.metric = %v", data["metric"])
	}
	if data["value"].(float64) != 1 {
		t.Fatalf("data.value = %v", data["value"])
	}
	if data["ts"] == "" {
		t.Fatal("data.ts must be set")
	}
	meta, ok := data["meta"].(map[string]any)
	if !ok {
		t.Fatalf("data.meta missing or wrong type: %T", data["meta"])
	}
	if meta["offering_id"] != offeringID.String() {
		t.Fatalf("meta.offering_id = %v", meta["offering_id"])
	}
	if meta["booking_id"] != bookingID.String() {
		t.Fatalf("meta.booking_id = %v", meta["booking_id"])
	}
	if meta["price_cents"].(float64) != 4200 {
		t.Fatalf("meta.price_cents = %v", meta["price_cents"])
	}
}

// Metering is off when the usage topic is unconfigured; otherwise exactly
// one extra outbox row is produced for the usage topic.
func TestUsageExtraToggle(t *testing.T) {
	offering := store.Offering{ID: uuid.New(), PriceCents: 100}
	off := &Service{Logger: zap.NewNop()}
	if got := off.UsageExtra("acme", uuid.New(), uuid.New(), offering); got != nil {
		t.Fatalf("UsageExtra with empty topic = %v, want nil", got)
	}
	on := &Service{UsageTopic: "opendesk.usage.events", Logger: zap.NewNop()}
	got := on.UsageExtra("acme", uuid.New(), uuid.New(), offering)
	if len(got) != 1 || got[0].Topic != "opendesk.usage.events" || len(got[0].Payload) == 0 {
		t.Fatalf("UsageExtra = %+v", got)
	}
}
