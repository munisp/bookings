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

// Reminder sends must also go through the NotifyPaced pacing wrapper
// (VOICE-SCALING §4: same carrier CPS + spam-reputation discipline as the
// waitlist campaign sends).
func TestReminderWorkflowSendsViaNotifyPaced(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	in := ReminderInput{
		BookingID:         "b-1",
		TenantID:          "t-1",
		TenantSlug:        "acme",
		ContactPhone:      "+15550001111",
		ContactName:       "Caller",
		StartsAt:          time.Now().Add(2 * time.Second),
		DevOverrideDelays: []time.Duration{time.Second},
	}

	env.RegisterActivityWithOptions(func(ctx context.Context, in ReminderInput) (string, error) {
		return "confirmed", nil
	}, activity.RegisterOptions{Name: ActivityGetBookingStatus})
	env.RegisterActivityWithOptions(func(ctx context.Context, req PacedSendRequest) error {
		return nil
	}, activity.RegisterOptions{Name: ActivityNotifyPaced})

	paced := make([]PacedSendRequest, 0)
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			paced = append(paced, args.Get(1).(PacedSendRequest))
		}).Return(nil).Once()

	env.ExecuteWorkflow(ReminderWorkflow, in)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Len(t, paced, 1, "exactly one paced send for the single reminder window")
	require.Equal(t, PacedSendReminder, paced[0].Kind)
	require.NotNil(t, paced[0].Reminder)
	require.Equal(t, time.Second.String(), paced[0].Reminder.Kind)
	require.Equal(t, in.BookingID, paced[0].Reminder.Input.BookingID)
	env.AssertExpectations(t)
}
