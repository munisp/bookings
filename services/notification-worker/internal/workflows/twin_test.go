package workflows

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// The 24h timer is auto-skipped by the test environment; the delete
// activity must fire exactly once with the twin input.
func TestTwinCleanupWorkflowDeletesAfterTTL(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	in := TwinCleanupInput{TenantID: "t-9", Slug: "acme-twin-x7k2p9", TwinOf: "acme"}
	env.RegisterActivityWithOptions(func(ctx context.Context, in TwinCleanupInput) error { return nil },
		activity.RegisterOptions{Name: ActivityDeleteTwinTenant})

	called := 0
	env.OnActivity(ActivityDeleteTwinTenant, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			called++
			got := args.Get(1).(TwinCleanupInput)
			require.Equal(t, "acme-twin-x7k2p9", got.Slug)
			require.Equal(t, "acme", got.TwinOf)
		}).Return(nil).Once()

	env.ExecuteWorkflow(TwinCleanupWorkflow, in)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, 1, called)
	env.AssertExpectations(t)
}
