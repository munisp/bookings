// GDPR data-subject workflows (SPEC-W3 §2 innovation 13):
//
//   - GdprExportWorkflow gathers everything the platform holds about a data
//     subject (identified by phone and/or e-mail) across booking,
//     conversation, payments (ledger) and the CRM, packs it into one JSON
//     bundle and uploads it to the MinIO `exports` bucket via a plain S3 PUT.
//     The workflow result is the object path (dev) — a presigned URL can be
//     issued from the same object in prod.
//   - GdprEraseWorkflow publishes a PrivacyEraseRequested tombstone
//     CloudEvent to opendesk.privacy.events (via Dapr pubsub); booking,
//     conversation and crm-sync consume it and anonymize/delete their copy
//     of the subject's data.
//
// Both workflows are started by booking-service's POST /v1/privacy/{export,
// erase} endpoints (manage_bookings, SPEC-W3 §2).
package workflows

import (
	"encoding/json"
	"time"

	"go.temporal.io/sdk/workflow"
)

// GdprInput is the input contract of GdprExportWorkflow/GdprEraseWorkflow.
// It mirrors booking-service's temporalclient.GdprRequest (JSON-compatible;
// duplicated per service-boundary rules).
type GdprInput struct {
	TenantID   string `json:"tenant_id"`
	TenantSlug string `json:"tenant_slug"`
	Phone      string `json:"phone,omitempty"`
	Email      string `json:"email,omitempty"`
}

// Contact returns the primary contact reference (phone preferred).
func (in GdprInput) Contact() string {
	if in.Phone != "" {
		return in.Phone
	}
	return in.Email
}

// GDPR activity names (registered by cmd/worker).
const (
	ActivityGdprCollectBookings      = "GdprCollectBookings"
	ActivityGdprCollectConversations = "GdprCollectConversations"
	ActivityGdprCollectLedger        = "GdprCollectLedger"
	ActivityGdprCollectCrmPerson     = "GdprCollectCrmPerson"
	ActivityGdprUploadExport         = "GdprUploadExport"
	ActivityGdprPublishErase         = "GdprPublishEraseTombstone"
)

// GdprExportBundle is the JSON document uploaded to MinIO.
type GdprExportBundle struct {
	Subject       GdprInput        `json:"subject"`
	Bookings      json.RawMessage `json:"bookings"`
	Conversations json.RawMessage `json:"conversations"`
	// Knowledge is intentionally absent: knowledge documents are tenant
	// content, not data-subject PII (SPEC-W3 §2: "knowledge n/a").
	LedgerBalance json.RawMessage `json:"ledger_balance"`
	CrmPerson     json.RawMessage `json:"crm_person"`
}

// GdprExportWorkflow runs the cross-service collection and returns the
// MinIO object path of the uploaded bundle.
func GdprExportWorkflow(ctx workflow.Context, in GdprInput) (string, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 45 * time.Second,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var bundle GdprExportBundle
	bundle.Subject = in
	if err := workflow.ExecuteActivity(ctx, ActivityGdprCollectBookings, in).Get(ctx, &bundle.Bookings); err != nil {
		return "", err
	}
	if err := workflow.ExecuteActivity(ctx, ActivityGdprCollectConversations, in).Get(ctx, &bundle.Conversations); err != nil {
		return "", err
	}
	if err := workflow.ExecuteActivity(ctx, ActivityGdprCollectLedger, in).Get(ctx, &bundle.LedgerBalance); err != nil {
		return "", err
	}
	if err := workflow.ExecuteActivity(ctx, ActivityGdprCollectCrmPerson, in).Get(ctx, &bundle.CrmPerson); err != nil {
		return "", err
	}

	var path string
	if err := workflow.ExecuteActivity(ctx, ActivityGdprUploadExport, bundle).Get(ctx, &path); err != nil {
		return "", err
	}
	return path, nil
}

// GdprEraseWorkflow publishes the erase tombstone; downstream consumers do
// the actual anonymization/deletion asynchronously.
func GdprEraseWorkflow(ctx workflow.Context, in GdprInput) error {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
	}
	ctx = workflow.WithActivityOptions(ctx, ao)
	return workflow.ExecuteActivity(ctx, ActivityGdprPublishErase, in).Get(ctx, nil)
}
