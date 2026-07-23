package activities

import (
	"context"
	"fmt"

	"github.com/opendesk/notification-worker/internal/workflows"
	"go.uber.org/zap"
)

// Geo-targeted campaign sends (SPEC-W8 A2). booking-service's
// GeoCampaignWorkflow schedules these through NotifyPaced (kind
// geo_campaign), so every campaign message is CPS-paced and sender-rotated
// exactly like the other outbound kinds.

// SendGeoCampaignMessage delivers one personalized campaign message on the
// requested channel:
//
//   - sms: routed through the channel router (MESSAGING_CHANNELS /
//     TENANT_CHANNEL_MAP — twilio native binding or the messaging-gateway
//     HTTP bindings termii/africastalking/whatsapp), with sender rotation;
//   - whatsapp / telegram: the messaging-gateway HTTP binding convention
//     ("bindings-"+channel, operation "post", {to, message} data), same as
//     the Nigeria providers in sendSMS.
func (a *Activities) SendGeoCampaignMessage(ctx context.Context, g workflows.PacedGeoCampaignSend) error {
	if g.Phone == "" {
		return fmt.Errorf("geo campaign send: phone is required (campaign %s)", g.CampaignID)
	}
	if g.Text == "" {
		return fmt.Errorf("geo campaign send: text is required (campaign %s)", g.CampaignID)
	}

	sender := a.TwilioFrom
	if a.Pacer != nil {
		if n := a.Pacer.NextSender(ctx); n != "" {
			sender = n
		}
	}

	switch g.Channel {
	case ChannelSMS:
		provider := a.Channels.Provider(ChannelSMS, g.TenantSlug)
		if err := a.sendSMS(ctx, provider, g.Phone, g.Text, sender); err != nil {
			return fmt.Errorf("%s binding: %w", provider, err)
		}
		a.Log.Info("geo campaign message sent", zap.String("campaign_id", g.CampaignID),
			zap.String("channel", g.Channel), zap.String("provider", provider),
			zap.String("phone", g.Phone), zap.String("sender_number", sender))
	case "whatsapp", "telegram":
		if err := a.Dapr.InvokeBinding(ctx, a.BindingName(g.Channel), "post", map[string]string{
			"to":      g.Phone,
			"message": g.Text,
		}, nil); err != nil {
			return fmt.Errorf("%s binding: %w", g.Channel, err)
		}
		a.Log.Info("geo campaign message sent", zap.String("campaign_id", g.CampaignID),
			zap.String("channel", g.Channel), zap.String("phone", g.Phone))
	default:
		return fmt.Errorf("geo campaign send: unknown channel %q (want whatsapp, telegram or sms)", g.Channel)
	}
	return nil
}
