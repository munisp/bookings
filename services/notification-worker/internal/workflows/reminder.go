package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

// ReminderWorkflow sends appointment reminders at T-24h and T-1h (SPEC §6).
// It reacts to the "booking-event" signal: `cancelled` stops the workflow,
// `rescheduled` re-arms the remaining timers against the new start time.
// Before every send it re-checks the booking status via GetBookingStatus so
// reminders never fire for cancelled bookings even if the signal was missed.
func ReminderWorkflow(ctx workflow.Context, in ReminderInput) error {
	logger := workflow.GetLogger(ctx)
	ctx = workflow.WithActivityOptions(ctx, sagaActivityOptions())

	state := "scheduled"
	if err := workflow.SetQueryHandler(ctx, QueryState, func() (string, error) {
		return state, nil
	}); err != nil {
		return err
	}

	offsets := in.DevOverrideDelays
	if len(offsets) == 0 {
		offsets = []time.Duration{24 * time.Hour, time.Hour}
	}
	sent := map[string]bool{}

	for { // restart loop for reschedule signals
		restart := false
		for _, off := range offsets {
			kind := off.String()
			if sent[kind] {
				continue
			}
			delay := in.StartsAt.Add(-off).Sub(workflow.Now(ctx))
			if delay < 0 {
				sent[kind] = true // reminder window already passed
				continue
			}

			timer := workflow.NewTimer(ctx, delay)
			sigCh := workflow.GetSignalChannel(ctx, SignalBookingEvent)
			var sig BookingEventSignal
			gotSignal := false

			sel := workflow.NewSelector(ctx)
			sel.AddFuture(timer, func(f workflow.Future) { /* timer fired */ })
			sel.AddReceive(sigCh, func(c workflow.ReceiveChannel, more bool) {
				c.Receive(ctx, &sig)
				gotSignal = true
			})
			sel.Select(ctx)

			if gotSignal {
				switch sig.Type {
				case "cancelled":
					state = "stopped:cancelled"
					logger.Info("reminder workflow stopped: booking cancelled", "booking_id", in.BookingID)
					return nil
				case "rescheduled":
					if !sig.NewStartsAt.IsZero() {
						in.StartsAt = sig.NewStartsAt
						state = "re-armed:rescheduled"
						restart = true
					}
				}
				if restart {
					break
				}
				continue
			}

			// Timer fired — verify the booking is still active before sending.
			var status string
			if err := workflow.ExecuteActivity(ctx, ActivityGetBookingStatus, in).Get(ctx, &status); err != nil {
				logger.Warn("GetBookingStatus failed; sending reminder anyway", "error", err)
			} else if status == "cancelled" || status == "no_show" {
				state = "stopped:" + status
				return nil
			}

			state = "sending:" + kind
			if err := workflow.ExecuteActivity(ctx, ActivitySendReminder, in, kind).Get(ctx, nil); err != nil {
				logger.Error("SendReminder failed", "kind", kind, "error", err)
			}
			sent[kind] = true
			state = "scheduled"
		}
		if !restart {
			break
		}
	}
	state = "done"
	return nil
}
