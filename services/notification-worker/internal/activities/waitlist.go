package activities

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"github.com/opendesk/notification-worker/internal/workflows"
)

// Waitlist backfill activities (SPEC-W3 §3 innovation 7).

var waitlistClaimEmail = template.Must(template.New("waitlist").Parse(
	`Hi {{.Name}}, a slot just opened up with {{.Tenant}} for the appointment you wanted (window starting {{.StartsAt}}). Claim it before it is gone: {{.Kind}}`))

// ListWaitlistEntries fetches the waiting waitlist entries of the offering
// from booking-service via Dapr service invocation
// (GET /v1/waitlist?offering_id&status=waiting, tenant via X-Tenant-Slug).
func (a *Activities) ListWaitlistEntries(ctx context.Context, in workflows.WaitlistBackfillInput) ([]workflows.WaitlistEntry, error) {
	var out struct {
		Entries []workflows.WaitlistEntry `json:"entries"`
	}
	method := fmt.Sprintf("v1/waitlist?offering_id=%s&status=waiting", url.QueryEscape(in.OfferingID))
	err := a.Dapr.InvokeServiceMethod(ctx, http.MethodGet, a.BookingAppID, method, nil,
		map[string]string{"X-Tenant-Slug": in.TenantSlug}, &out)
	if err != nil {
		return nil, err
	}
	return out.Entries, nil
}

// claimLink builds the user-facing claim URL for a waitlist entry. The
// claim token is the single-use capability checked transactionally by
// booking-service's POST /v1/waitlist/{id}/claim.
func (a *Activities) claimLink(tenantSlug string, e workflows.WaitlistEntry) string {
	base := strings.TrimRight(a.PublicBaseURL, "/")
	if base == "" {
		base = "http://localhost:9080"
	}
	return fmt.Sprintf("%s/p/%s/claim?entry=%s&token=%s", base, tenantSlug, e.ID, e.ClaimToken)
}

// SendWaitlistClaimNotification notifies one waiting entry via the existing
// email/sms output bindings with its claim token + link. Waitlist entries
// carry a phone only, so in practice the Twilio SMS binding fires; the SMTP
// path is skipped for empty recipients by notify.
func (a *Activities) SendWaitlistClaimNotification(ctx context.Context, in workflows.WaitlistBackfillInput, e workflows.WaitlistEntry) error {
	td := templateData{
		Name:     e.ContactName,
		StartsAt: e.WindowStart.Format(time.RFC1123),
		Kind:     a.claimLink(in.TenantSlug, e),
		Tenant:   in.TenantSlug,
	}
	return a.notify(ctx, td, "", e.ContactPhone, "A slot opened up — claim it now", waitlistClaimEmail)
}
