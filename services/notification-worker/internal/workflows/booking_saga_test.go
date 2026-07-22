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
	"go.temporal.io/sdk/workflow"
)

// registerSagaStubs registers no-op activities under the saga's activity
// names (so OnActivity can mock them) plus stub child workflows.
func registerSagaStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(func(ctx context.Context, in SagaInput) error { return nil },
		activity.RegisterOptions{Name: ActivityReserveSlot})
	env.RegisterActivityWithOptions(func(ctx context.Context, in SagaInput) (string, error) { return "", nil },
		activity.RegisterOptions{Name: ActivityHoldDeposit})
	env.RegisterActivityWithOptions(func(ctx context.Context, in SagaInput) error { return nil },
		activity.RegisterOptions{Name: ActivityConfirmBooking})
	env.RegisterActivityWithOptions(func(ctx context.Context, req PacedSendRequest) error { return nil },
		activity.RegisterOptions{Name: ActivityNotifyPaced})
	env.RegisterActivityWithOptions(func(ctx context.Context, in SagaInput, reason string) error { return nil },
		activity.RegisterOptions{Name: ActivityReleaseSlot})
	env.RegisterActivityWithOptions(func(ctx context.Context, in SagaInput, holdID string) error { return nil },
		activity.RegisterOptions{Name: ActivityVoidHold})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in ReminderInput) error { return nil },
		workflow.RegisterOptions{Name: "ReminderWorkflow"})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in NoShowInput) error { return nil },
		workflow.RegisterOptions{Name: "NoShowFollowupWorkflow"})
	// Industry pack child workflow stubs (SPEC-CRM §C2).
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in SalonDepositInput) error { return nil },
		workflow.RegisterOptions{Name: "SalonDepositWorkflow"})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in ClinicIntakeInput) error { return nil },
		workflow.RegisterOptions{Name: "ClinicIntakeWorkflow"})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in ConsultancyFollowupInput) error { return nil },
		workflow.RegisterOptions{Name: "ConsultancyFollowupWorkflow"})
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, in SupportEscalationInput) error { return nil },
		workflow.RegisterOptions{Name: "SupportEscalationWorkflow"})
}

func sagaTestInput() SagaInput {
	return SagaInput{
		BookingID:    "b-1",
		TenantID:     "t-1",
		TenantSlug:   "acme",
		ContactName:  "Jane",
		ContactEmail: "jane@example.com",
		ContactPhone: "+15551234567",
		StartsAt:     time.Now().Add(48 * time.Hour),
		EndsAt:       time.Now().Add(49 * time.Hour),
		PriceCents:   5000,
		Currency:     "USD",
	}
}

func TestBookingSagaWorkflow_HappyPath(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(BookingSagaWorkflow)
	registerSagaStubs(env)

	var order []string
	track := func(name string) func(mock.Arguments) {
		return func(args mock.Arguments) { order = append(order, name) }
	}
	env.OnActivity(ActivityReserveSlot, mock.Anything, mock.Anything).
		Run(track("reserve")).Return(nil).Once()
	env.OnActivity(ActivityHoldDeposit, mock.Anything, mock.Anything).
		Run(track("hold")).Return("hold-1", nil).Once()
	env.OnActivity(ActivityConfirmBooking, mock.Anything, mock.Anything).
		Run(track("confirm")).Return(nil).Once()
	// The confirmation is CPS-paced: the saga must call NotifyPaced with the
	// confirmation kind carrying the full saga input.
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.MatchedBy(func(req PacedSendRequest) bool {
		return req.Kind == PacedSendConfirmation && req.Confirmation != nil &&
			req.Confirmation.Input.BookingID == "b-1" &&
			req.Confirmation.Input.ContactEmail == "jane@example.com"
	})).Run(track("notify")).Return(nil).Once()

	env.ExecuteWorkflow(BookingSagaWorkflow, sagaTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"reserve", "hold", "confirm", "notify"}, order,
		"happy path must run forward steps only, no compensation")
	env.AssertExpectations(t)
}

func TestBookingSagaWorkflow_CompensatesInReverseOrder(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(BookingSagaWorkflow)
	registerSagaStubs(env)

	var order []string
	track := func(name string) func(mock.Arguments) {
		return func(args mock.Arguments) { order = append(order, name) }
	}
	env.OnActivity(ActivityReserveSlot, mock.Anything, mock.Anything).
		Run(track("reserve")).Return(nil).Once()
	env.OnActivity(ActivityHoldDeposit, mock.Anything, mock.Anything).
		Run(track("hold")).Return("hold-1", nil).Once()
	env.OnActivity(ActivityConfirmBooking, mock.Anything, mock.Anything).
		Return(errors.New("booking service exploded")).Once()
	env.OnActivity(ActivityVoidHold, mock.Anything, mock.Anything, mock.Anything).
		Run(track("void")).Return(nil).Once()
	env.OnActivity(ActivityReleaseSlot, mock.Anything, mock.Anything, mock.Anything).
		Run(track("release")).Return(nil).Once()

	env.ExecuteWorkflow(BookingSagaWorkflow, sagaTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Equal(t, []string{"reserve", "hold", "void", "release"}, order,
		"compensations must run in reverse order: VoidHold then ReleaseSlot")
	env.AssertExpectations(t)
}

func TestBookingSagaWorkflow_ReserveSlotFailureNoCompensation(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(BookingSagaWorkflow)
	registerSagaStubs(env)

	var order []string
	env.OnActivity(ActivityReserveSlot, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) { order = append(order, "reserve") }).
		Return(errors.New("slot taken")).Once()

	env.ExecuteWorkflow(BookingSagaWorkflow, sagaTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Equal(t, []string{"reserve"}, order,
		"nothing to compensate when the first step fails")
	env.AssertExpectations(t)
}
