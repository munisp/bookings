package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// Digital-twin cleanup (SPEC-W3 §3 innovation 12): a twin tenant lives 24h,
// then TwinCleanupWorkflow deletes it via identity-service's
// DELETE /v1/tenants/{slug} (Dapr service invocation). The workflow is
// started by identity-service's POST /internal/tenants/{slug}/twin through
// the worker's POST /dev/trigger-twin-cleanup sidecar endpoint.
const (
	ActivityDeleteTwinTenant = "DeleteTwinTenant"
	// TwinTTL is the default digital-twin lifetime (SPEC-W3: 24h).
	TwinTTL = 24 * time.Hour
)

// TwinCleanupInput starts a TwinCleanupWorkflow.
type TwinCleanupInput struct {
	TenantID string `json:"tenant_id"`
	Slug     string `json:"slug"`
	TwinOf   string `json:"twin_of"`
	// DelayHours overrides TwinTTL when > 0 (dev/manual testing).
	DelayHours float64 `json:"delay_hours,omitempty"`
}

// TwinCleanupWorkflow sleeps for the twin TTL and then deletes the twin
// tenant. Deletion is retried by the standard activity retry policy; if the
// tenant is already gone the activity succeeds idempotently.
func TwinCleanupWorkflow(ctx workflow.Context, in TwinCleanupInput) error {
	logger := workflow.GetLogger(ctx)
	delay := TwinTTL
	if in.DelayHours > 0 {
		delay = time.Duration(in.DelayHours * float64(time.Hour))
	}
	if err := workflow.NewTimer(ctx, delay).Get(ctx, nil); err != nil {
		return err
	}
	ctx = workflow.WithActivityOptions(ctx, sagaActivityOptions())
	if err := workflow.ExecuteActivity(ctx, ActivityDeleteTwinTenant, in).Get(ctx, nil); err != nil {
		return err
	}
	logger.Info("digital twin tenant deleted", "slug", in.Slug, "twin_of", in.TwinOf)
	return nil
}
