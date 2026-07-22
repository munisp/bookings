// Package notifyoutbox consumes opendesk.notifications.outbox — the
// fire-and-forget notification command topic (SPEC §4). Wave 5 #7 adds the
// first producer: booking-service publishes
// com.opendesk.notifications.SendPortalCode when a customer requests a
// portal login code; this consumer is the delivery half — it owns the
// smtp/twilio Dapr bindings, exactly like the workflow activities.
package notifyoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/opendesk/notification-worker/internal/daprc"
	"github.com/segmentio/kafka-go"
	"go.uber.org/zap"
)

// BindingSender delivers notification commands through the Dapr output
// bindings (bindings-smtp / bindings-twilio) — the same path the workflow
// activities use.
type BindingSender struct {
	Dapr          *daprc.Client
	SMTPBinding   string
	TwilioBinding string
	SMTPFrom      string
	TwilioFrom    string
}

// Send implements Sender.
func (s BindingSender) Send(ctx context.Context, channel, destination, subject, text string) error {
	switch channel {
	case "email":
		return s.Dapr.InvokeBinding(ctx, s.SMTPBinding, "create", text, map[string]string{
			"emailTo":   destination,
			"emailFrom": s.SMTPFrom,
			"subject":   subject,
		})
	case "sms":
		return s.Dapr.InvokeBinding(ctx, s.TwilioBinding, "create", text, map[string]string{
			"toNumber":   destination,
			"fromNumber": s.TwilioFrom,
		})
	default:
		return fmt.Errorf("unknown channel %q", channel)
	}
}

// EventTypeSendPortalCode is the portal login code command (Wave 5 #7).
const EventTypeSendPortalCode = "com.opendesk.notifications.SendPortalCode"

// Sender delivers one text message over a channel ("sms" or "email").
type Sender interface {
	Send(ctx context.Context, channel, destination, subject, text string) error
}

// Consumer reads the notifications outbox topic and delivers commands.
type Consumer struct {
	reader *kafka.Reader
	sender Sender
	log    *zap.Logger
}

// New builds the consumer (explicit commits, like the signal bridge).
func New(brokers []string, topic, group string, sender Sender, log *zap.Logger) *Consumer {
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        brokers,
			Topic:          topic,
			GroupID:        group,
			MinBytes:       1,
			MaxBytes:       10 << 20,
			CommitInterval: 0,
			StartOffset:    kafka.FirstOffset,
		}),
		sender: sender,
		log:    log,
	}
}

// Run consumes until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	c.log.Info("notifications outbox consumer started")
	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("fetch message: %w", err)
		}
		if err := c.Process(ctx, msg.Value); err != nil {
			// A failed send is logged and acknowledged (never hot-looped);
			// the customer can simply request a fresh code.
			c.log.Error("notification command failed; acknowledging anyway",
				zap.String("key", string(msg.Key)), zap.Error(err))
		}
		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			c.log.Error("commit failed", zap.Error(err))
		}
	}
}

// Close releases the reader.
func (c *Consumer) Close() error { return c.reader.Close() }

// envelope is the CloudEvents wrapper of notification commands.
type envelope struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// Process handles one raw command payload (exported for testing).
func (c *Consumer) Process(ctx context.Context, raw []byte) error {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		c.log.Warn("malformed notification command; skipping", zap.Error(err))
		return nil
	}
	switch env.Type {
	case EventTypeSendPortalCode:
		return c.sendPortalCode(ctx, env.Data)
	default:
		return nil // unknown commands are acknowledged (forward-compatible)
	}
}

// sendPortalCode delivers the 6-digit portal login code. The plaintext code
// exists only in this payload and in the message to the customer — the
// booking DB holds its SHA-256 hash.
func (c *Consumer) sendPortalCode(ctx context.Context, data map[string]any) error {
	channel, _ := data["channel"].(string)
	dest, _ := data["destination"].(string)
	code, _ := data["code"].(string)
	if (channel != "sms" && channel != "email") || dest == "" || code == "" {
		return fmt.Errorf("SendPortalCode: invalid payload (channel=%q)", channel)
	}
	text := fmt.Sprintf("Your OpenDesk booking portal code is %s. It is valid for 10 minutes.", code)
	subject := "Your booking portal login code"
	if err := c.sender.Send(ctx, channel, dest, subject, text); err != nil {
		return fmt.Errorf("SendPortalCode send: %w", err)
	}
	c.log.Info("portal code delivered", zap.String("channel", channel))
	return nil
}
