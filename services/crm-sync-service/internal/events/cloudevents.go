// Package events parses CloudEvents 1.0 envelopes per SPEC §4:
// {specversion, id, source, type, subject, time, tenantid (ext), data}.
// Event type strings and data field names mirror the producers:
//   - identity-service  (com.opendesk.identity.TenantProvisioned)
//   - booking-service   (com.opendesk.booking.Booking*, see internal/bookingops)
//   - voice-agent-runtime (com.opendesk.conversation.ToolInvoked, app/tools.py)
package events

import (
	"encoding/json"
	"fmt"
	"time"
)

// CloudEvent is the canonical envelope used on every Kafka topic.
type CloudEvent struct {
	SpecVersion string         `json:"specversion"`
	ID          string         `json:"id"`
	Source      string         `json:"source"`
	Type        string         `json:"type"`
	Subject     string         `json:"subject,omitempty"` // tenant slug
	Time        time.Time      `json:"time"`
	TenantID    string         `json:"tenantid,omitempty"`
	Data        map[string]any `json:"data"`
}

// Event type constants consumed from the backbone.
const (
	TypeTenantProvisioned = "com.opendesk.identity.TenantProvisioned"

	TypeBookingCreated     = "com.opendesk.booking.BookingCreated"
	TypeBookingConfirmed   = "com.opendesk.booking.BookingConfirmed"
	TypeBookingRescheduled = "com.opendesk.booking.BookingRescheduled"
	TypeBookingCancelled   = "com.opendesk.booking.BookingCancelled"

	TypeToolInvoked = "com.opendesk.conversation.ToolInvoked"
)

// TenantProvisionedData mirrors identity-service createTenant's payload:
// {"tenant_id","slug","name","plan"}.
type TenantProvisionedData struct {
	TenantID string `json:"tenant_id"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Plan     string `json:"plan"`
}

// BookingData mirrors booking-service bookingops.marshalEvent payloads.
// BookingCreated/Confirmed/Rescheduled carry the full contact block
// (MarshalBookingEvent); BookingCancelled omits contact_name/contact_id/
// offering_name and adds "reason" instead.
type BookingData struct {
	BookingID    string    `json:"booking_id"`
	StartsAt     time.Time `json:"starts_at"`
	EndsAt       time.Time `json:"ends_at"`
	Status       string    `json:"status"`
	Source       string    `json:"source"`
	OfferingID   string    `json:"offering_id"`
	OfferingName string    `json:"offering_name"`
	TeamMemberID string    `json:"team_member_id"`
	ContactID    string    `json:"contact_id"`
	ContactName  string    `json:"contact_name"`
	Phone        string    `json:"phone"`
	Email        string    `json:"email"`
	PriceCents   int64     `json:"price_cents"`
	Currency     string    `json:"currency"`
	Reason       string    `json:"reason"`
}

// ToolInvokedData mirrors voice-agent-runtime _emit_tool_event payloads:
// {"conversationId","tool","status","detail"}.
type ToolInvokedData struct {
	ConversationID string         `json:"conversationId"`
	Tool           string         `json:"tool"`
	Status         string         `json:"status"`
	Detail         map[string]any `json:"detail"`
}

// Parse unmarshals one Kafka message value into a CloudEvent envelope.
func Parse(raw []byte) (CloudEvent, error) {
	var evt CloudEvent
	if err := json.Unmarshal(raw, &evt); err != nil {
		return evt, fmt.Errorf("unmarshal cloudevent: %w", err)
	}
	if evt.Type == "" {
		return evt, fmt.Errorf("cloudevent %q has no type", evt.ID)
	}
	return evt, nil
}

// DataAs re-marshals evt.Data into the typed payload struct T.
func DataAs[T any](evt CloudEvent) (T, error) {
	var out T
	b, err := json.Marshal(evt.Data)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("decode %s data: %w", evt.Type, err)
	}
	return out, nil
}

// New builds a CloudEvent envelope (used for opendesk.crm.events emission).
func New(id, source, eventType, subject, tenantID string, data map[string]any) CloudEvent {
	return CloudEvent{
		SpecVersion: "1.0",
		ID:          id,
		Source:      source,
		Type:        eventType,
		Subject:     subject,
		Time:        time.Now().UTC(),
		TenantID:    tenantID,
		Data:        data,
	}
}
