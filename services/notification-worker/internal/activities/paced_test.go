package activities

import (
	"context"
	"testing"
	"time"

	"github.com/opendesk/notification-worker/internal/daprc"
	"github.com/opendesk/notification-worker/internal/pacer"
	"github.com/opendesk/notification-worker/internal/workflows"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func pacedTestActivities(p *pacer.Pacer) *Activities {
	a := New(daprc.New("127.0.0.1", 1), "booking", "payments", "identity",
		"bindings-smtp", "bindings-twilio", "no-reply@test", "+10000000000", "", IndustryDeps{}, zap.NewNop())
	a.Pacer = p
	return a
}

// The waitlist claim request with no phone/email renders and skips both
// bindings, so it exercises NotifyPaced's pacing + dispatch without a
// Dapr sidecar.
func waitlistClaimReq() workflows.PacedSendRequest {
	return workflows.PacedSendRequest{
		Kind: workflows.PacedSendWaitlistClaim,
		Waitlist: &workflows.PacedWaitlistSend{
			Input: workflows.WaitlistBackfillInput{BookingID: "b-1", TenantSlug: "acme", OfferingID: "o-1"},
			Entry: workflows.WaitlistEntry{ID: "e-a", ContactName: "Caller", WindowStart: time.Now()},
		},
	}
}

// NotifyPaced acquires a CPS token before dispatching: with 1 CPS and a
// burst of 1 the second send must wait for a refill.
func TestNotifyPacedPacesBeforeSend(t *testing.T) {
	p := pacer.New(pacer.Config{CPS: 1.0, Burst: 1, Backend: "local"}, zap.NewNop())
	a := pacedTestActivities(p)
	ctx := context.Background()

	start := time.Now()
	require.NoError(t, a.NotifyPaced(ctx, waitlistClaimReq()))
	require.NoError(t, a.NotifyPaced(ctx, waitlistClaimReq()))
	require.GreaterOrEqual(t, time.Since(start), 800*time.Millisecond,
		"second send at 1 CPS / burst 1 must be paced ~1s")
}

// Dispatch validation: unknown kinds and missing payloads fail fast.
func TestNotifyPacedDispatchValidation(t *testing.T) {
	a := pacedTestActivities(nil) // nil pacer: pacing disabled
	ctx := context.Background()

	require.ErrorContains(t, a.NotifyPaced(ctx, workflows.PacedSendRequest{Kind: "bogus"}), "unknown send kind")
	require.ErrorContains(t, a.NotifyPaced(ctx, workflows.PacedSendRequest{Kind: workflows.PacedSendWaitlistClaim}), "missing waitlist payload")
	require.ErrorContains(t, a.NotifyPaced(ctx, workflows.PacedSendRequest{Kind: workflows.PacedSendReminder}), "missing reminder payload")

	// Valid reminder dispatch (no recipients → no binding calls).
	require.NoError(t, a.NotifyPaced(ctx, workflows.PacedSendRequest{
		Kind:     workflows.PacedSendReminder,
		Reminder: &workflows.PacedReminderSend{Input: workflows.ReminderInput{BookingID: "b-1"}, Kind: "24h0m0s"},
	}))
}
