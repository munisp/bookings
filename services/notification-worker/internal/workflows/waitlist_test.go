package workflows

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func waitlistTestInput() WaitlistBackfillInput {
	return WaitlistBackfillInput{
		BookingID:  "b-9",
		TenantID:   "t-1",
		TenantSlug: "acme",
		OfferingID: "o-1",
	}
}

func waitlistTestEntries(n int) []WaitlistEntry {
	out := make([]WaitlistEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, WaitlistEntry{
			ID:           "e-" + string(rune('a'+i)),
			OfferingID:   "o-1",
			ContactName:  "Caller",
			ContactPhone: "+1555000" + string(rune('0'+i)),
			WindowStart:  time.Now().Add(24 * time.Hour),
			WindowEnd:    time.Now().Add(48 * time.Hour),
			Status:       "waiting",
			ClaimToken:   "tok-" + string(rune('a'+i)),
		})
	}
	return out
}

// Happy path: five waiting entries → exactly the top 3 are notified.
func TestWaitlistBackfillWorkflowNotifiesTop3(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivityWithOptions(func(ctx context.Context, in WaitlistBackfillInput) ([]WaitlistEntry, error) {
		return waitlistTestEntries(5), nil
	}, activity.RegisterOptions{Name: ActivityListWaitlistEntries})
	env.RegisterActivityWithOptions(func(ctx context.Context, in WaitlistBackfillInput, e WaitlistEntry) error {
		return nil
	}, activity.RegisterOptions{Name: ActivitySendWaitlistClaimNote})

	notified := make([]string, 0)
	env.OnActivity(ActivitySendWaitlistClaimNote, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			notified = append(notified, args.Get(2).(WaitlistEntry).ID)
		}).Return(nil)

	env.ExecuteWorkflow(WaitlistBackfillWorkflow, waitlistTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"e-a", "e-b", "e-c"}, notified, "exactly the top 3, FIFO order")
}

// Empty waitlist → no notifications, no error.
func TestWaitlistBackfillWorkflowEmptyWaitlist(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()

	env.RegisterActivityWithOptions(func(ctx context.Context, in WaitlistBackfillInput) ([]WaitlistEntry, error) {
		return nil, nil
	}, activity.RegisterOptions{Name: ActivityListWaitlistEntries})
	env.RegisterActivityWithOptions(func(ctx context.Context, in WaitlistBackfillInput, e WaitlistEntry) error {
		return nil
	}, activity.RegisterOptions{Name: ActivitySendWaitlistClaimNote})

	env.OnActivity(ActivitySendWaitlistClaimNote, mock.Anything, mock.Anything, mock.Anything).
		Panic("must not notify when the waitlist is empty")

	env.ExecuteWorkflow(WaitlistBackfillWorkflow, waitlistTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}

// One failing notification must not block the remaining candidates.
func TestWaitlistBackfillWorkflowContinuesAfterNotifyFailure(t *testing.T) {
	env := (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	entries := waitlistTestEntries(2)

	env.RegisterActivityWithOptions(func(ctx context.Context, in WaitlistBackfillInput) ([]WaitlistEntry, error) {
		return entries, nil
	}, activity.RegisterOptions{Name: ActivityListWaitlistEntries})
	env.RegisterActivityWithOptions(func(ctx context.Context, in WaitlistBackfillInput, e WaitlistEntry) error {
		return nil
	}, activity.RegisterOptions{Name: ActivitySendWaitlistClaimNote})

	entryWithID := func(id string) interface{} {
		return mock.MatchedBy(func(e WaitlistEntry) bool { return e.ID == id })
	}
	env.OnActivity(ActivitySendWaitlistClaimNote, mock.Anything, mock.Anything, entryWithID(entries[0].ID)).
		Return(errors.New("twilio down")).Once()
	env.OnActivity(ActivitySendWaitlistClaimNote, mock.Anything, mock.Anything, entryWithID(entries[1].ID)).
		Return(nil).Once()

	env.ExecuteWorkflow(WaitlistBackfillWorkflow, waitlistTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}
