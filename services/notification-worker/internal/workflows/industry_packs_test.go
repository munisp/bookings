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

// registerPackStubs registers no-op activities under the pack workflows'
// activity names (so OnActivity can mock them).
func registerPackStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(func(ctx context.Context, in SalonDepositInput) (bool, error) { return true, nil },
		activity.RegisterOptions{Name: ActivityVerifyDepositHold})
	env.RegisterActivityWithOptions(func(ctx context.Context, req PacedSendRequest) error { return nil },
		activity.RegisterOptions{Name: ActivityNotifyPaced})
	env.RegisterActivityWithOptions(func(ctx context.Context, in SalonDepositInput) error { return nil },
		activity.RegisterOptions{Name: ActivityChargeNoShowFee})
	env.RegisterActivityWithOptions(func(ctx context.Context, in ClinicIntakeInput) error { return nil },
		activity.RegisterOptions{Name: ActivityCreateStaffAlertTask})
	env.RegisterActivityWithOptions(func(ctx context.Context, in ConsultancyFollowupInput) error { return nil },
		activity.RegisterOptions{Name: ActivityCreateCRMFollowupTask})
}

func clinicTestInput() ClinicIntakeInput {
	return ClinicIntakeInput{
		BookingID:    "b-clinic-1",
		TenantID:     "t-1",
		TenantSlug:   "acme-clinic",
		ContactName:  "Pat Patient",
		ContactEmail: "pat@example.com",
		StartsAt:     time.Now().Add(73 * time.Hour), // T-72h reminder due in 1h
	}
}

// Intake-timeout path: no IntakeCompleted signal ever arrives, so the T-72h
// reminder fires and the T-2h deadline raises a staff alert task.
func TestClinicIntakeWorkflow_TimeoutAlertsStaff(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ClinicIntakeWorkflow)
	registerPackStubs(env)

	var order []string
	track := func(name string) func(mock.Arguments) {
		return func(args mock.Arguments) { order = append(order, name) }
	}
	// The T-72h intake reminder is CPS-paced via NotifyPaced.
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.MatchedBy(func(req PacedSendRequest) bool {
		return req.Kind == PacedSendIntakeReminder && req.Intake != nil &&
			req.Intake.Input.BookingID == "b-clinic-1"
	})).Run(track("intake-reminder")).Return(nil).Once()
	env.OnActivity(ActivityCreateStaffAlertTask, mock.Anything, mock.Anything).
		Run(track("staff-alert")).Return(nil).Once()

	env.ExecuteWorkflow(ClinicIntakeWorkflow, clinicTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"intake-reminder", "staff-alert"}, order,
		"timeout path must send the T-72h intake reminder and then alert staff at T-2h")
	env.AssertExpectations(t)
}

// Happy path: the patient completes intake after the reminder — no staff
// alert is filed.
func TestClinicIntakeWorkflow_IntakeCompletedSignal(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ClinicIntakeWorkflow)
	registerPackStubs(env)

	var order []string
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.MatchedBy(func(req PacedSendRequest) bool {
		return req.Kind == PacedSendIntakeReminder && req.Intake != nil
	})).Run(func(args mock.Arguments) { order = append(order, "intake-reminder") }).Return(nil).Once()

	// Patient completes the intake form 2 hours after the reminder.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalIntakeCompleted, nil)
	}, 3*time.Hour)

	env.ExecuteWorkflow(ClinicIntakeWorkflow, clinicTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"intake-reminder"}, order,
		"completed intake must not raise a staff alert")
	env.AssertExpectations(t)
}

func salonTestInput() SalonDepositInput {
	return SalonDepositInput{
		BookingID:               "b-salon-1",
		TenantID:                "t-1",
		TenantSlug:              "acme-salon",
		ContactName:             "Jane",
		ContactEmail:            "jane@example.com",
		StartsAt:                time.Now().Add(time.Hour),
		EndsAt:                  time.Now().Add(2 * time.Hour),
		HoldID:                  "7c9e1781-f647-4e28-9f9b-9a4d5c226001",
		DepositCents:            1500,
		NoShowFeeCents:          2000,
		Currency:                "USD",
		CancellationWindowHours: 24,
	}
}

// Deposit no-show path: hold verified, NoShow signal arrives before the
// appointment end → the pack no-show fee is charged from the hold.
func TestSalonDepositWorkflow_NoShowChargesFee(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(SalonDepositWorkflow)
	registerPackStubs(env)

	var order []string
	track := func(name string) func(mock.Arguments) {
		return func(args mock.Arguments) { order = append(order, name) }
	}
	env.OnActivity(ActivityVerifyDepositHold, mock.Anything, mock.Anything).
		Run(track("verify")).Return(true, nil).Once()
	env.OnActivity(ActivityChargeNoShowFee, mock.Anything, mock.Anything).
		Run(track("no-show-fee")).Return(nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalNoShow, nil)
	}, 90*time.Minute)

	env.ExecuteWorkflow(SalonDepositWorkflow, salonTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"verify", "no-show-fee"}, order,
		"no-show path must verify the hold and charge the no-show fee")
	env.AssertExpectations(t)
}

// Missing deposit inside the cancellation window → reminder; no NoShow
// signal before the appointment end → completes without charging a fee.
func TestSalonDepositWorkflow_ReminderWhenDepositMissing(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(SalonDepositWorkflow)
	registerPackStubs(env)

	in := salonTestInput()
	in.HoldID = "" // no hold was placed

	var order []string
	track := func(name string) func(mock.Arguments) {
		return func(args mock.Arguments) { order = append(order, name) }
	}
	env.OnActivity(ActivityVerifyDepositHold, mock.Anything, mock.Anything).
		Run(track("verify")).Return(false, nil).Once()
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.MatchedBy(func(req PacedSendRequest) bool {
		return req.Kind == PacedSendDepositReminder && req.Deposit != nil && req.Deposit.Input.BookingID == in.BookingID
	})).Run(track("deposit-reminder")).Return(nil).Once()

	env.ExecuteWorkflow(SalonDepositWorkflow, in)
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"verify", "deposit-reminder"}, order,
		"missing deposit inside the window must trigger the reminder only")
	env.AssertExpectations(t)
}

func consultancyTestInput() ConsultancyFollowupInput {
	return ConsultancyFollowupInput{
		BookingID:    "b-consult-1",
		TenantID:     "t-1",
		TenantSlug:   "acme-consult",
		ContactName:  "Dana Founder",
		ContactEmail: "dana@example.com",
		EndsAt:       time.Now().Add(time.Hour), // session ends in 1h
	}
}

// Full consultancy path: after the session ends the follow-up email and the
// CRM task fire, then the T+7d proposal reminder. Both outbound sends are
// CPS-paced via NotifyPaced (follow_up / proposal_reminder kinds).
func TestConsultancyFollowupWorkflow_PacedFollowupAndProposal(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(ConsultancyFollowupWorkflow)
	registerPackStubs(env)

	var order []string
	track := func(name string) func(mock.Arguments) {
		return func(args mock.Arguments) { order = append(order, name) }
	}
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.MatchedBy(func(req PacedSendRequest) bool {
		return req.Kind == PacedSendFollowUp && req.FollowUp != nil &&
			req.FollowUp.Input.BookingID == "b-consult-1"
	})).Run(track("follow-up")).Return(nil).Once()
	env.OnActivity(ActivityCreateCRMFollowupTask, mock.Anything, mock.Anything).
		Run(track("crm-task")).Return(nil).Once()
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.MatchedBy(func(req PacedSendRequest) bool {
		return req.Kind == PacedSendProposalReminder && req.Proposal != nil &&
			req.Proposal.Input.BookingID == "b-consult-1"
	})).Run(track("proposal-reminder")).Return(nil).Once()

	env.ExecuteWorkflow(ConsultancyFollowupWorkflow, consultancyTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"follow-up", "crm-task", "proposal-reminder"}, order,
		"session end → paced follow-up, CRM task, paced T+7d proposal reminder")
	env.AssertExpectations(t)
}

func supportTestInput() SupportEscalationInput {
	return SupportEscalationInput{
		BookingID:             "b-support-1",
		TenantID:              "t-1",
		TenantSlug:            "acme-support",
		ContactName:           "Irate Customer",
		ContactEmail:          "irate@example.com",
		CreatedAt:             time.Now(),
		FirstResponseSLAHours: 4,
	}
}

// SLA breach path: no Responded signal within the 4h SLA → the escalation
// (owner email + CRM priority event) is CPS-paced via NotifyPaced
// (staff_alert kind).
func TestSupportEscalationWorkflow_PacedEscalationOnSLABreach(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(SupportEscalationWorkflow)
	registerPackStubs(env)

	var escalated bool
	env.OnActivity(ActivityNotifyPaced, mock.Anything, mock.MatchedBy(func(req PacedSendRequest) bool {
		return req.Kind == PacedSendStaffAlert && req.StaffAlert != nil &&
			req.StaffAlert.Input.BookingID == "b-support-1" &&
			req.StaffAlert.Input.FirstResponseSLAHours == 4
	})).Run(func(args mock.Arguments) { escalated = true }).Return(nil).Once()

	env.ExecuteWorkflow(SupportEscalationWorkflow, supportTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.True(t, escalated, "SLA breach must escalate via NotifyPaced staff_alert")
	env.AssertExpectations(t)
}

// Responded-within-SLA path: the Responded signal before the deadline closes
// the ticket watch without any escalation.
func TestSupportEscalationWorkflow_RespondedWithinSLA(t *testing.T) {
	ts := &testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(SupportEscalationWorkflow)
	registerPackStubs(env)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalResponded, nil)
	}, time.Hour)

	env.ExecuteWorkflow(SupportEscalationWorkflow, supportTestInput())
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}
