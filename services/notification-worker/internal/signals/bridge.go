// Package signals bridges opendesk.booking.events (Kafka) to Temporal
// signals for the per-booking child workflows started by the booking saga
// (SPEC-CRM §C2):
//
//	BookingCancelled → signal pack-{bookingId} and reminder-{bookingId} with
//	                   "booking-event" {type: "cancelled"}
//	BookingNoShow    → signal pack-{bookingId} with "NoShow"
//
// Delivery is best-effort: the workflows re-check booking state via
// activities, so a missed signal is not fatal. Workflows that are not
// running (completed or never started) are logged and acknowledged — never
// retried and never dead-lettered.
package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/opendesk/notification-worker/internal/workflows"
	"github.com/segmentio/kafka-go"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.uber.org/zap"
)

// Signaller abstracts the Temporal client (client.Client satisfies it).
type Signaller interface {
	SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error
}

// WorkflowStarter abstracts Temporal workflow starts (client.Client
// satisfies it via ExecuteWorkflow) — used to kick off
// WaitlistBackfillWorkflow on BookingCancelled (SPEC-W3 §3 innovation 7).
type WorkflowStarter interface {
	ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflowType interface{}, args ...interface{}) (client.WorkflowRun, error)
}

// Bridge consumes booking events and forwards them as Temporal signals.
type Bridge struct {
	reader    *kafka.Reader
	temporal  Signaller
	starter   WorkflowStarter // nil disables waitlist backfill starts
	taskQueue string
	log       *zap.Logger
}

// Option customizes a Bridge.
type Option func(*Bridge)

// WithBackfillStarter enables WaitlistBackfillWorkflow starts on
// BookingCancelled events.
func WithBackfillStarter(starter WorkflowStarter, taskQueue string) Option {
	return func(b *Bridge) {
		b.starter = starter
		b.taskQueue = taskQueue
	}
}

// New builds the bridge. brokers is a direct broker list (e.g. kafka:9092);
// config mirrors booking-service's consumer (explicit commits, no DLQ —
// unknown/completed workflows are logged and acknowledged).
func New(brokers []string, topic, group string, temporal Signaller, log *zap.Logger, opts ...Option) *Bridge {
	b := &Bridge{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        brokers,
			Topic:          topic,
			GroupID:        group,
			MinBytes:       1,
			MaxBytes:       10 << 20,
			CommitInterval: 0, // explicit commits only, after processing
			StartOffset:    kafka.FirstOffset,
		}),
		temporal: temporal,
		log:      log,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Run consumes until ctx is cancelled.
func (b *Bridge) Run(ctx context.Context) error {
	b.log.Info("booking-events signal bridge started")
	for {
		msg, err := b.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("fetch message: %w", err)
		}
		if err := b.Process(ctx, msg.Value); err != nil {
			// Transient signalling failure (Temporal unreachable). Signals
			// are best-effort: log and ack instead of redelivering forever.
			b.log.Error("signal delivery failed; acknowledging anyway",
				zap.String("key", string(msg.Key)), zap.Error(err))
		}
		if err := b.reader.CommitMessages(ctx, msg); err != nil {
			b.log.Error("commit failed", zap.Error(err))
		}
	}
}

// Close releases the reader.
func (b *Bridge) Close() error { return b.reader.Close() }

// eventEnvelope is the CloudEvents envelope of booking events.
type eventEnvelope struct {
	Type     string         `json:"type"`
	Subject  string         `json:"subject"`  // tenant slug
	TenantID string         `json:"tenantid"` // CloudEvents extension
	Data     map[string]any `json:"data"`
}

func (env eventEnvelope) bookingID() string {
	if v, ok := env.Data["booking_id"].(string); ok {
		return v
	}
	return ""
}

// Process handles one raw event payload (exported for testing).
func (b *Bridge) Process(ctx context.Context, raw []byte) error {
	var env eventEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		b.log.Warn("malformed booking event; skipping", zap.Error(err))
		return nil
	}
	id := env.bookingID()
	if id == "" {
		return nil // events without a booking reference need no signals
	}

	switch env.Type {
	case "com.opendesk.booking.BookingCancelled":
		sigErr := b.signalAll(ctx,
			[]string{"pack-" + id, "reminder-" + id},
			workflows.SignalBookingEvent, workflows.BookingEventSignal{Type: "cancelled"})
		// SPEC-W3 §3 innovation 7: every cancellation is also a backfill
		// opportunity for the offering's waitlist.
		startErr := b.startBackfill(ctx, env)
		return errors.Join(sigErr, startErr)
	case "com.opendesk.booking.BookingNoShow":
		return b.signalAll(ctx,
			[]string{"pack-" + id},
			workflows.SignalNoShow, nil)
	default:
		return nil // other event types need no workflow signals
	}
}

// startBackfill starts one WaitlistBackfillWorkflow per cancelled booking.
// Redelivered events hit the WorkflowExecutionAlreadyStarted case and are
// acknowledged (the workflow ID is derived from the booking ID).
func (b *Bridge) startBackfill(ctx context.Context, env eventEnvelope) error {
	if b.starter == nil {
		return nil
	}
	offeringID, _ := env.Data["offering_id"].(string)
	if offeringID == "" {
		return nil // cannot query the waitlist without the offering
	}
	in := workflows.WaitlistBackfillInput{
		BookingID:  env.bookingID(),
		TenantID:   env.TenantID,
		TenantSlug: env.Subject,
		OfferingID: offeringID,
	}
	_, err := b.starter.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        "waitlist-backfill-" + env.bookingID(),
		TaskQueue: b.taskQueue,
	}, "WaitlistBackfillWorkflow", in)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &alreadyStarted) || strings.Contains(err.Error(), "already started") {
			b.log.Info("WaitlistBackfillWorkflow already running; event acknowledged",
				zap.String("booking_id", env.bookingID()))
			return nil
		}
		return fmt.Errorf("start WaitlistBackfillWorkflow: %w", err)
	}
	b.log.Info("WaitlistBackfillWorkflow started",
		zap.String("booking_id", env.bookingID()), zap.String("offering_id", offeringID))
	return nil
}

// signalAll delivers one signal to each target workflow. A workflow that is
// not running (NotFound) is logged and skipped — it is not an error and the
// message must not be retried or dead-lettered.
func (b *Bridge) signalAll(ctx context.Context, workflowIDs []string, signal string, payload any) error {
	var errs []error
	for _, id := range workflowIDs {
		err := b.temporal.SignalWorkflow(ctx, id, "", signal, payload)
		if err == nil {
			b.log.Info("workflow signalled",
				zap.String("workflow_id", id), zap.String("signal", signal))
			continue
		}
		var notFound *serviceerror.NotFound
		if errors.As(err, &notFound) || strings.Contains(err.Error(), "workflow not found") {
			b.log.Info("workflow not running; signal skipped",
				zap.String("workflow_id", id), zap.String("signal", signal))
			continue
		}
		errs = append(errs, fmt.Errorf("signal %s to %s: %w", signal, id, err))
	}
	return errors.Join(errs...)
}
