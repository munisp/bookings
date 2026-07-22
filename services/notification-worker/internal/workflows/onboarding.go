package workflows

import (
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// TenantOnboardingWorkflow provisions the per-tenant resources of SPEC §6:
// Keycloak group, Permify tenant/schema, Postgres seed data and the
// OpenSearch index alias. Every step is idempotent so the workflow can be
// retried safely; steps run in dependency order.
func TenantOnboardingWorkflow(ctx workflow.Context, in OnboardingInput) error {
	logger := workflow.GetLogger(ctx)
	ctx = workflow.WithActivityOptions(ctx, sagaActivityOptions())

	state := "started"
	if err := workflow.SetQueryHandler(ctx, QueryState, func() (string, error) {
		return state, nil
	}); err != nil {
		return err
	}

	steps := []struct {
		name     string
		activity string
	}{
		{"keycloak-group", ActivityEnsureKeycloakGroup},
		{"permify-tenant", ActivityEnsurePermifyTenant},
		{"postgres-seed", ActivitySeedTenantData},
		{"search-alias", ActivityEnsureSearchAlias},
		// SPEC-CRM §C2: seed pack offerings, knowledge docs and terminology.
		{"industry-pack", ActivityApplyIndustryPack},
	}
	for _, step := range steps {
		state = "running:" + step.name
		if err := workflow.ExecuteActivity(ctx, step.activity, in).Get(ctx, nil); err != nil {
			state = "failed:" + step.name
			logger.Error("onboarding step failed", "step", step.name, "error", err)
			return temporal.NewApplicationError("onboarding step "+step.name+" failed", "OnboardingStepFailed", err)
		}
		state = "done:" + step.name
	}
	state = "completed"
	return nil
}
