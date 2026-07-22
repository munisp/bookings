// GDPR activities (SPEC-W3 §2 innovation 13): cross-service data-subject
// collection for GdprExportWorkflow, MinIO upload of the export bundle, and
// the PrivacyEraseRequested tombstone publish of GdprEraseWorkflow.
package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/opendesk/notification-worker/internal/workflows"
)

// GdprDeps bundles the configuration of the GDPR activities. Set by main
// after New (kept out of New's positional parameter list).
type GdprDeps struct {
	ConversationAppID string // Dapr app-id of conversation-service
	PubSubName        string // Dapr pubsub component (pubsub-kafka)
	PrivacyTopic      string // opendesk.privacy.events
	S3Endpoint        string // http://minio:9000 (path-style)
	S3Region          string // us-east-1
	S3AccessKey       string // MinIO access key (env creds)
	S3SecretKey       string // MinIO secret key (env creds)
	S3ExportsBucket   string // exports
}

// ---------------------------------------------------------------------------
// Collectors (Dapr service invocation, GET)
// ---------------------------------------------------------------------------

// GdprCollectBookings fetches the subject's bookings from booking-service
// (GET /v1/bookings?contact=, X-Tenant-Slug scoped).
func (a *Activities) GdprCollectBookings(ctx context.Context, in workflows.GdprInput) (json.RawMessage, error) {
	var out json.RawMessage
	method := "v1/bookings?contact=" + url.QueryEscape(in.Contact())
	err := a.Dapr.InvokeServiceMethod(ctx, http.MethodGet, a.BookingAppID, method, nil,
		map[string]string{"X-Tenant-Slug": in.TenantSlug}, &out)
	if err != nil {
		return nil, fmt.Errorf("collect bookings: %w", err)
	}
	return out, nil
}

// GdprCollectConversations fetches the subject's conversations from
// conversation-service (GET /v1/conversations?tenant=&contact=).
func (a *Activities) GdprCollectConversations(ctx context.Context, in workflows.GdprInput) (json.RawMessage, error) {
	var out json.RawMessage
	method := fmt.Sprintf("v1/conversations?tenant=%s&contact=%s",
		url.QueryEscape(in.TenantID), url.QueryEscape(in.Contact()))
	err := a.Dapr.InvokeServiceMethod(ctx, http.MethodGet, a.Gdpr.ConversationAppID, method, nil, nil, &out)
	if err != nil {
		return nil, fmt.Errorf("collect conversations: %w", err)
	}
	return out, nil
}

// GdprCollectLedger fetches the tenant's ledger balance from payments-service
// (GET /v1/accounts/{tenant_id}/balance). The ledger is tenant-scoped and
// carries no per-contact PII; the balance is included as account context.
func (a *Activities) GdprCollectLedger(ctx context.Context, in workflows.GdprInput) (json.RawMessage, error) {
	var out json.RawMessage
	err := a.Dapr.InvokeServiceMethod(ctx, http.MethodGet, a.PaymentsAppID,
		"v1/accounts/"+url.PathEscape(in.TenantID)+"/balance", nil, nil, &out)
	if err != nil {
		return nil, fmt.Errorf("collect ledger balance: %w", err)
	}
	return out, nil
}

// GdprCollectCrmPerson looks up the subject's Twenty person record via
// crm-sync-service (GET /v1/people/lookup?email=|phone=). Returns
// {"person": null} when the subject has no CRM record.
func (a *Activities) GdprCollectCrmPerson(ctx context.Context, in workflows.GdprInput) (json.RawMessage, error) {
	var out json.RawMessage
	q := url.Values{}
	if in.Email != "" {
		q.Set("email", in.Email)
	}
	if in.Phone != "" {
		q.Set("phone", in.Phone)
	}
	err := a.Dapr.InvokeServiceMethod(ctx, http.MethodGet, a.Industry.CRMSyncAppID,
		"v1/people/lookup?"+q.Encode(), nil, nil, &out)
	if err != nil {
		return nil, fmt.Errorf("collect crm person: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Export upload (plain S3 PUT against MinIO)
// ---------------------------------------------------------------------------

// GdprUploadExport marshals the bundle and uploads it to the MinIO exports
// bucket via a SigV4-signed plain HTTP PUT. Returns the object path
// (bucket/key) — dev hand-off is the path; prod can layer presigned GET URLs.
func (a *Activities) GdprUploadExport(ctx context.Context, bundle workflows.GdprExportBundle) (string, error) {
	body, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal export bundle: %w", err)
	}
	key := fmt.Sprintf("%s/%s-%s.json",
		bundle.Subject.TenantID, time.Now().UTC().Format("20060102T150405Z"), uuid.NewString()[:8])
	if err := s3Put(ctx, a.hc, a.Gdpr.S3Endpoint, a.Gdpr.S3Region,
		a.Gdpr.S3ExportsBucket, key, body, a.Gdpr.S3AccessKey, a.Gdpr.S3SecretKey); err != nil {
		return "", fmt.Errorf("upload export bundle: %w", err)
	}
	path := a.Gdpr.S3ExportsBucket + "/" + key
	a.Log.Info("gdpr export uploaded")
	return path, nil
}

// ---------------------------------------------------------------------------
// Erase tombstone publish
// ---------------------------------------------------------------------------

// GdprPublishEraseTombstone publishes the PrivacyEraseRequested CloudEvent to
// opendesk.privacy.events. Booking (anonymize contacts), conversation
// (delete turns) and crm-sync (delete Twenty person) consume it.
func (a *Activities) GdprPublishEraseTombstone(ctx context.Context, in workflows.GdprInput) error {
	if in.Phone == "" && in.Email == "" {
		return fmt.Errorf("erase requires phone or email")
	}
	evt := map[string]any{
		"specversion": "1.0",
		"id":          uuid.NewString(),
		"source":      "notification-worker",
		"type":        "PrivacyEraseRequested",
		"subject":     in.TenantSlug,
		"time":        time.Now().UTC().Format(time.RFC3339),
		"tenantid":    in.TenantID,
		"data": map[string]any{
			"phone":     in.Phone,
			"email":     in.Email,
			"tenant_id": in.TenantID,
		},
	}
	if err := a.Dapr.PublishEvent(ctx, a.Gdpr.PubSubName, a.Gdpr.PrivacyTopic, evt); err != nil {
		return fmt.Errorf("publish erase tombstone: %w", err)
	}
	return nil
}
