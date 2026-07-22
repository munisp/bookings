package workflows

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// registerNoShowStubs registers no-op activities under the no-show
// workflow's activity names (so OnActivity can mock them). The follow-up
// message is an outbound send and goes through the NotifyPaced pacing
// wrapper (VOICE-SCALING §4).
func registerNoShowStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(func(ctx context.Context, in NoShowInput) (string, error) {
		return "confirmed", nil
	}, activity.RegisterOptions{Name: ActivityGetBookingStatus})
	env.RegisterActivityWithOptions(func(ctx context.Context, in NoShowInput) error { return nil },
		activity.RegisterOptions{Name: ActivityMarkNoShow})
	env.RegisterActivityWithOptions(func(ctx context.Context, req PacedSendRequest) error { return nil },
		activity.RegisterOptions{Name: ActivityNotifyPaced})
}

func noshowTestInput() NoShowInput {
	return NoShowInput{
		BookingID:    "b-1",
		TenantID:     "t-1",
		TenantSlug:   "acme",
		ContactPhone: "+15550001111",
		ContactEmail: "jane@example.com",
		EndsAt:       time.Now().Add(-3 * time.Hour), // grace period already over
	}
}

// No-show path: booking still confirmed after the grace period → mark
// no_show and pace the follow-up message through NotifyPaced.
func TestNoShowFollowupWorkflow_SendsFollowupViaNotifyPaced(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	registerNoShowStubs(env)

	in := noshowTestInput()
	marked := false
	env.OnActivity(ActivityMarkNoShow, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { marked = true }).Return(nil).Once()
	paced := make([]PacedSendRequest, 0)
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			paced = append(paced, args.Get(1).(PacedSendRequest))
		}).Return(nil).Once()

	env.ExecuteWorkflow(NoShowFollowupWorkflow, in)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.True(t, marked, "a still-confirmed booking must be marked no_show")
	require.Len(t, paced, 1, "exactly one paced follow-up send")
	require.Equal(t, PacedSendNoShow, paced[0].Kind,
		"the no-show follow-up must be wrapped by NotifyPaced")
	require.NotNil(t, paced[0].NoShow)
	require.Equal(t, in.BookingID, paced[0].NoShow.Input.BookingID)
	require.Equal(t, in.ContactEmail, paced[0].NoShow.Input.ContactEmail)
	env.AssertExpectations(t)
}

// Booking completed (not "confirmed") → no follow-up is sent.
func TestNoShowFollowupWorkflow_SkipsWhenNotConfirmed(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	registerNoShowStubs(env)

	statusChecked := false
	env.OnActivity(ActivityGetBookingStatus, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { statusChecked = true }).Return("completed", nil).Once()
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.Anything).
		Panic("must not send a follow-up for a completed booking")

	env.ExecuteWorkflow(NoShowFollowupWorkflow, noshowTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.True(t, statusChecked, "the workflow must check the booking status before deciding")
}
