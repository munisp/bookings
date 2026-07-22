// Event -> Twenty object mapping (pure functions, unit-tested).
package twentyc

import (
	"fmt"
	"strings"
	"time"

	"github.com/opendesk/crm-sync-service/internal/events"
)

// TenantDomain is the synthetic domainName for a tenant's Twenty Company:
// slug + ".opendesk.local" (SPEC-CRM §B).
func TenantDomain(slug string) string {
	return slug + ".opendesk.local"
}

// CompanyUpsert is the body for creating/updating a Twenty Company from a
// TenantProvisioned event.
type CompanyUpsert struct {
	Name       string `json:"name"`
	DomainName Links  `json:"domainName"`
}

// Links is Twenty's LINKS field shape (v1.x).
type Links struct {
	PrimaryLinkURL string `json:"primaryLinkUrl"`
}

// CompanyFromTenant maps TenantProvisioned -> Company upsert body.
func CompanyFromTenant(d events.TenantProvisionedData) CompanyUpsert {
	return CompanyUpsert{
		Name:       d.Name,
		DomainName: Links{PrimaryLinkURL: "https://" + TenantDomain(d.Slug)},
	}
}

// PersonUpsert is the body for creating/updating a Twenty Person.
type PersonUpsert struct {
	Name   FullName `json:"name"`
	Emails *Emails  `json:"emails,omitempty"`
	Phones *Phones  `json:"phones,omitempty"`
}

// FullName is Twenty's FULL_NAME field shape.
type FullName struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
}

// Emails is Twenty's EMAILS field shape.
type Emails struct {
	PrimaryEmail string `json:"primaryEmail"`
}

// Phones is Twenty's PHONES field shape.
type Phones struct {
	PrimaryPhoneNumber string `json:"primaryPhoneNumber"`
}

// PersonFromContact maps booking contact fields -> Person upsert body.
// The contact name is split on the first space (first/last).
func PersonFromContact(name, email, phone string) PersonUpsert {
	p := PersonUpsert{Name: SplitName(name)}
	if email != "" {
		p.Emails = &Emails{PrimaryEmail: email}
	}
	if phone != "" {
		p.Phones = &Phones{PrimaryPhoneNumber: phone}
	}
	return p
}

// SplitName splits a display name into first/last at the first space.
func SplitName(name string) FullName {
	name = strings.TrimSpace(name)
	if name == "" {
		return FullName{FirstName: "Unknown"}
	}
	first, last, found := strings.Cut(name, " ")
	if !found {
		return FullName{FirstName: name}
	}
	return FullName{FirstName: first, LastName: strings.TrimSpace(last)}
}

// TaskTitle renders "{offering} appointment at {starts_at}" (SPEC-CRM §B).
// starts_at is rendered RFC3339 in UTC for stable, parseable titles.
func TaskTitle(offering string, startsAt time.Time) string {
	if offering == "" {
		offering = "Appointment"
	}
	return fmt.Sprintf("%s appointment at %s", offering, startsAt.UTC().Format(time.RFC3339))
}

// TaskCreate is the body for creating a Twenty Task.
type TaskCreate struct {
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	DueAt  string `json:"dueAt,omitempty"`
	Status string `json:"status,omitempty"`
}

// TaskFromBooking maps a booking event -> Task create body linked to a person.
func TaskFromBooking(d events.BookingData) TaskCreate {
	return TaskCreate{
		Title: TaskTitle(d.OfferingName, d.StartsAt),
		Body: fmt.Sprintf("OpenDesk booking %s (%s) — contact %s, source %s, price %d %s.",
			d.BookingID, d.Status, d.ContactName, d.Source, d.PriceCents, d.Currency),
		DueAt:  FormatTime(d.StartsAt),
		Status: "TODO",
	}
}

// CancelNote renders the note appended to a task body on BookingCancelled.
func CancelNote(reason string) string {
	if reason == "" {
		reason = "no reason given"
	}
	return "Booking cancelled in OpenDesk: " + reason
}

// AIBookingNote is the Note body for AI-receptionist bookings (SPEC-CRM §B).
const AIBookingNote = "Booked via AI receptionist"

// FormatTime renders a timestamp in Twenty's accepted RFC3339 form (UTC).
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }
