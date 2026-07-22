package workflows

// Outbound CPS pacing (docs/VOICE-SCALING.md §4 telephony plane).
//
// Workflows are deterministic and must never sleep or rate-limit inline, so
// pacing lives activity-side: every outbound send goes through the single
// NotifyPaced activity, which acquires a CPS token and rotates the sender
// number (internal/pacer) BEFORE dispatching to the underlying send
// activity. Workflows only build a PacedSendRequest.
const (
	// ActivityNotifyPaced is the name of the pacing wrapper activity.
	ActivityNotifyPaced = "NotifyPaced"

	// PacedSendWaitlistClaim routes to SendWaitlistClaimNotification
	// (SPEC-W3 §3 innovation 7 waitlist backfill).
	PacedSendWaitlistClaim = "waitlist_claim"
	// PacedSendReminder routes to SendReminder (T-24h / T-1h reminders).
	PacedSendReminder = "reminder"
)

// PacedSendRequest is the payload of the NotifyPaced activity: which send
// to perform after the CPS token is granted, plus its arguments.
type PacedSendRequest struct {
	Kind     string             `json:"kind"` // PacedSend* constant
	Waitlist *PacedWaitlistSend `json:"waitlist,omitempty"`
	Reminder *PacedReminderSend `json:"reminder,omitempty"`
}

// PacedWaitlistSend carries the SendWaitlistClaimNotification arguments.
type PacedWaitlistSend struct {
	Input WaitlistBackfillInput `json:"input"`
	Entry WaitlistEntry         `json:"entry"`
}

// PacedReminderSend carries the SendReminder arguments.
type PacedReminderSend struct {
	Input ReminderInput `json:"input"`
	Kind  string        `json:"kind"` // e.g. "24h0m0s", "1h0m0s"
}
