package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// noShowGrace is how long after the appointment end we wait before deciding
// the guest did not show up.
const noShowGrace = 2 * time.Hour

// NoShowFollowupWorkflow waits until the appointment end (+grace), checks
// the booking status and, when it is still `confirmed` (i.e. never
// completed/cancelled), marks it no_show and sends a follow-up message
// (SPEC §6).
func NoShowFollowupWorkflow(ctx workflow.Context, in NoShowInput) error {
	logger := workflow.GetLogger(ctx)
	ctx = workflow.WithActivityOptions(ctx, sagaActivityOptions())

	state := "waiting"
	if err := workflow.SetQueryHandler(ctx, QueryState, func() (string, error) {
		return state, nil
	}); err != nil {
		return err
	}

	delay := in.EndsAt.Add(noShowGrace).Sub(workflow.Now(ctx))
	if delay > 0 {
		timer := workflow.NewTimer(ctx, delay)
		sigCh := workflow.GetSignalChannel(ctx, SignalBookingEvent)
		var sig BookingEventSignal
		gotSignal := false
		sel := workflow.NewSelector(ctx)
		sel.AddFuture(timer, func(f workflow.Future) {})
		sel.AddReceive(sigCh, func(c workflow.ReceiveChannel, more bool) {
			c.Receive(ctx, &sig)
			gotSignal = true
		})
		sel.Select(ctx)
		if gotSignal && sig.Type == "cancelled" {
			state = "stopped:cancelled"
			return nil
		}
	}

	state = "checking-status"
	var status string
	if err := workflow.ExecuteActivity(ctx, ActivityGetBookingStatus, in).Get(ctx, &status); err != nil {
		state = "failed:status-check"
		return err
	}
	if status != "confirmed" {
		state = "done:" + status
		return nil
	}

	state = "marking-no-show"
	if err := workflow.ExecuteActivity(ctx, ActivityMarkNoShow, in).Get(ctx, nil); err != nil {
		state = "failed:mark-no-show"
		return err
	}

	state = "sending-followup"
	if err := workflow.ExecuteActivity(ctx, ActivitySendNoShowFollow, in).Get(ctx, nil); err != nil {
		logger.Error("no-show follow-up notification failed", "error", err)
	}
	state = "done:no-show"
	return nil
}
