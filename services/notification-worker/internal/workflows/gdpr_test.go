package workflows

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func gdprTestInput() GdprInput {
	return GdprInput{
		TenantID:   "t-1",
		TenantSlug: "acme",
		Phone:      "+15551234567",
		Email:      "jane@example.com",
	}
}

// registerGdprStubs registers no-op activities under the GDPR activity names.
func registerGdprStubs(env *testsuite.TestWorkflowEnvironment) {
	raw := func(ctx context.Context, in GdprInput) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}
	env.RegisterActivityWithOptions(raw, activity.RegisterOptions{Name: ActivityGdprCollectBookings})
	env.RegisterActivityWithOptions(raw, activity.RegisterOptions{Name: ActivityGdprCollectConversations})
	env.RegisterActivityWithOptions(raw, activity.RegisterOptions{Name: ActivityGdprCollectLedger})
	env.RegisterActivityWithOptions(raw, activity.RegisterOptions{Name: ActivityGdprCollectCrmPerson})
	env.RegisterActivityWithOptions(func(ctx context.Context, b GdprExportBundle) (string, error) {
		return "exports/t-1/bundle.json", nil
	}, activity.RegisterOptions{Name: ActivityGdprUploadExport})
	env.RegisterActivityWithOptions(func(ctx context.Context, in GdprInput) error { return nil },
		activity.RegisterOptions{Name: ActivityGdprPublishErase})
}

func TestGdprExportWorkflow_CollectsAndUploads(t *testing.T) {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	registerGdprStubs(env)
	in := gdprTestInput()

	env.OnActivity(ActivityGdprCollectBookings, mock.Anything, in).
		Return(json.RawMessage(`{"bookings":[]}`), nil).Once()
	env.OnActivity(ActivityGdprCollectConversations, mock.Anything, in).
		Return(json.RawMessage(`{"conversations":[]}`), nil).Once()
	env.OnActivity(ActivityGdprCollectLedger, mock.Anything, in).
		Return(json.RawMessage(`{"balance_cents":0}`), nil).Once()
	env.OnActivity(ActivityGdprCollectCrmPerson, mock.Anything, in).
		Return(json.RawMessage(`{"person":{"id":"p-1"}}`), nil).Once()
	env.OnActivity(ActivityGdprUploadExport, mock.Anything, mock.MatchedBy(func(b GdprExportBundle) bool {
		return b.Subject.TenantID == "t-1" &&
			string(b.Bookings) == `{"bookings":[]}` &&
			string(b.CrmPerson) == `{"person":{"id":"p-1"}}`
	})).Return("exports/t-1/bundle.json", nil).Once()

	env.ExecuteWorkflow(GdprExportWorkflow, in)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	var path string
	require.NoError(t, env.GetWorkflowResult(&path))
	require.Equal(t, "exports/t-1/bundle.json", path)
	env.AssertExpectations(t)
}

func TestGdprExportWorkflow_CollectorFailurePropagates(t *testing.T) {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	registerGdprStubs(env)
	in := gdprTestInput()

	env.OnActivity(ActivityGdprCollectBookings, mock.Anything, in).
		Return(json.RawMessage(nil), errTestCollector)
	env.OnActivity(ActivityGdprCollectConversations, mock.Anything, in).
		Return(json.RawMessage(`{}`), nil)
	env.OnActivity(ActivityGdprCollectLedger, mock.Anything, in).
		Return(json.RawMessage(`{}`), nil)
	env.OnActivity(ActivityGdprCollectCrmPerson, mock.Anything, in).
		Return(json.RawMessage(`{}`), nil)

	env.ExecuteWorkflow(GdprExportWorkflow, in)
	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
}

var errTestCollector = &collectorError{"boom"}

type collectorError struct{ msg string }

func (e *collectorError) Error() string { return e.msg }

func TestGdprEraseWorkflow_PublishesTombstone(t *testing.T) {
	testSuite := &testsuite.WorkflowTestSuite{}
	env := testSuite.NewTestWorkflowEnvironment()
	registerGdprStubs(env)
	in := gdprTestInput()

	env.OnActivity(ActivityGdprPublishErase, mock.Anything, in).Return(nil).Once()

	env.ExecuteWorkflow(GdprEraseWorkflow, in)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestGdprInput_ContactPrefersPhone(t *testing.T) {
	require.Equal(t, "+1555", GdprInput{Phone: "+1555", Email: "a@b.c"}.Contact())
	require.Equal(t, "a@b.c", GdprInput{Email: "a@b.c"}.Contact())
}
