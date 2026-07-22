package activities

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/opendesk/notification-worker/internal/workflows"
)

// DeleteTwinTenant deletes an expired digital-twin tenant via
// identity-service's DELETE /v1/tenants/{slug} (Dapr service invocation).
// Twin slugs carry the "-twin-" marker, so identity's permify-free guard
// allows the deletion (see identity-service twin.go). A 404 is success —
// the twin may already have been removed manually.
func (a *Activities) DeleteTwinTenant(ctx context.Context, in workflows.TwinCleanupInput) error {
	if !strings.Contains(in.Slug, "-twin-") {
		// Defence in depth: this activity must never delete a real tenant.
		return fmt.Errorf("refusing to delete non-twin tenant %q", in.Slug)
	}
	err := a.Dapr.InvokeServiceMethod(ctx, http.MethodDelete, a.IdentityAppID, "v1/tenants/"+in.Slug, nil, nil, nil)
	if err != nil && strings.Contains(err.Error(), "404") {
		return nil
	}
	return err
}
