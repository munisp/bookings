// Package outbox implements the transactional-outbox dispatcher
// (SPEC §6/§9): poll unsent rows, publish via Dapr pubsub, mark sent.
package outbox

import (
	"context"
	"time"

	"github.com/opendesk/booking-service/internal/daprc"
	"github.com/opendesk/booking-service/internal/store"
	"go.uber.org/zap"
)

// Dispatcher drains the outbox table to Kafka via Dapr pubsub.
type Dispatcher struct {
	store    *store.Store
	dapr     *daprc.Client
	pubsub   string
	interval time.Duration
	log      *zap.Logger
}

// New builds the dispatcher.
func New(st *store.Store, d *daprc.Client, pubsub string, interval time.Duration, log *zap.Logger) *Dispatcher {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Dispatcher{store: st, dapr: d, pubsub: pubsub, interval: interval, log: log}
}

// Run polls until ctx is cancelled (SPEC §9: at-least-once; consumers must
// be idempotent — a row is only marked sent after a successful publish).
func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	d.drain(ctx) // catch up immediately at boot
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.drain(ctx)
		}
	}
}

func (d *Dispatcher) drain(ctx context.Context) {
	events, err := d.store.FetchUnsentOutbox(ctx, 100)
	if err != nil {
		d.log.Error("outbox fetch failed", zap.Error(err))
		return
	}
	for _, e := range events {
		if err := d.dapr.PublishEvent(ctx, d.pubsub, e.Topic, e.Payload); err != nil {
			d.log.Error("outbox publish failed; will retry",
				zap.String("id", e.ID.String()), zap.String("topic", e.Topic), zap.Error(err))
			return // retry the batch on the next tick, preserving order
		}
		if err := d.store.MarkOutboxSent(ctx, e.ID); err != nil {
			d.log.Error("outbox mark-sent failed", zap.String("id", e.ID.String()), zap.Error(err))
			return
		}
	}
	if len(events) > 0 {
		d.log.Debug("outbox drained", zap.Int("published", len(events)))
	}
}
