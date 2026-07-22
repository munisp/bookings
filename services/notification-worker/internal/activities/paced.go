package activities

import (
	"context"
	"fmt"

	"github.com/opendesk/notification-worker/internal/workflows"
)

// Outbound CPS pacing (docs/VOICE-SCALING.md §4 telephony plane).

// NotifyPaced is the single entry point for outbound sends from workflows.
// It first acquires a token from the worker's CPS pacer (internal/pacer:
// fleet-wide Lua token bucket in redis, or a process-local limiter) — the
// pacing knob is simultaneously the carrier CPS ceiling and the
// spam-reputation discipline — and only then invokes the requested send
// activity. Workflows stay deterministic; all waiting happens here,
// activity-side.
//
// The sender rotation itself happens inside notify(): every paced send
// picks the next OUTBOUND_FROM_NUMBERS entry and puts it in the binding
// payload metadata.
func (a *Activities) NotifyPaced(ctx context.Context, req workflows.PacedSendRequest) error {
	if a.Pacer != nil {
		if err := a.Pacer.Wait(ctx); err != nil {
			return fmt.Errorf("pacer wait: %w", err)
		}
	}
	switch req.Kind {
	case workflows.PacedSendWaitlistClaim:
		if req.Waitlist == nil {
			return fmt.Errorf("NotifyPaced %s: missing waitlist payload", req.Kind)
		}
		return a.SendWaitlistClaimNotification(ctx, req.Waitlist.Input, req.Waitlist.Entry)
	case workflows.PacedSendReminder:
		if req.Reminder == nil {
			return fmt.Errorf("NotifyPaced %s: missing reminder payload", req.Kind)
		}
		return a.SendReminder(ctx, req.Reminder.Input, req.Reminder.Kind)
	default:
		return fmt.Errorf("NotifyPaced: unknown send kind %q", req.Kind)
	}
}
